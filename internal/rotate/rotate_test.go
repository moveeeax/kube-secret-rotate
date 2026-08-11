package rotate

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

const ns = "prod"

func secret(name string, data map[string][]byte) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Type:       corev1.SecretTypeOpaque,
		Data:       data,
	}
}

func deployEnv(name, secretName string) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name: "app",
						Env: []corev1.EnvVar{{
							Name: "PW",
							ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
								LocalObjectReference: corev1.LocalObjectReference{Name: secretName},
								Key:                  "password",
							}},
						}},
					}},
				},
			},
		},
	}
}

func statefulVolume(name, secretName string) *appsv1.StatefulSet {
	return &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: appsv1.StatefulSetSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "db"}},
					Volumes: []corev1.Volume{{
						Name:         "certs",
						VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: secretName}},
					}},
				},
			},
		},
	}
}

func TestChecksumStableAndSensitive(t *testing.T) {
	a := Checksum(map[string][]byte{"a": []byte("1"), "b": []byte("2")})
	b := Checksum(map[string][]byte{"b": []byte("2"), "a": []byte("1")})
	if a != b {
		t.Fatal("checksum must not depend on map order")
	}
	c := Checksum(map[string][]byte{"a": []byte("1"), "b": []byte("3")})
	if a == c {
		t.Fatal("checksum must change when a value changes")
	}
}

func TestRunRotatesOnlyImpactedWorkloads(t *testing.T) {
	client := fake.NewSimpleClientset(
		secret("db-creds", map[string][]byte{"password": []byte("old")}),
		deployEnv("api", "db-creds"),           // impacted via env
		statefulVolume("postgres", "db-creds"), // impacted via volume
		deployEnv("frontend", "other-secret"),  // untouched
	)

	res, err := Run(context.Background(), client, Options{
		Namespace: ns,
		Secret:    "db-creds",
		Data:      map[string][]byte{"password": []byte("new")},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.Updated {
		t.Fatal("expected secret to be updated")
	}
	if len(res.Impacts) != 2 {
		t.Fatalf("expected 2 impacts, got %d: %+v", len(res.Impacts), res.Impacts)
	}

	// Secret data actually changed.
	sec, _ := client.CoreV1().Secrets(ns).Get(context.Background(), "db-creds", metav1.GetOptions{})
	if string(sec.Data["password"]) != "new" {
		t.Fatalf("secret not updated, got %q", sec.Data["password"])
	}

	// Impacted workloads carry the checksum annotation.
	api, _ := client.AppsV1().Deployments(ns).Get(context.Background(), "api", metav1.GetOptions{})
	if got := api.Spec.Template.Annotations[AnnotationKey]; got != res.Checksum {
		t.Fatalf("api deployment not stamped: %q", got)
	}
	pg, _ := client.AppsV1().StatefulSets(ns).Get(context.Background(), "postgres", metav1.GetOptions{})
	if got := pg.Spec.Template.Annotations[AnnotationKey]; got != res.Checksum {
		t.Fatalf("postgres statefulset not stamped: %q", got)
	}

	// Unrelated workload is left completely alone.
	fe, _ := client.AppsV1().Deployments(ns).Get(context.Background(), "frontend", metav1.GetOptions{})
	if _, ok := fe.Spec.Template.Annotations[AnnotationKey]; ok {
		t.Fatal("frontend should not have been rolled")
	}
}

func TestDryRunMutatesNothing(t *testing.T) {
	client := fake.NewSimpleClientset(
		secret("db-creds", map[string][]byte{"password": []byte("old")}),
		deployEnv("api", "db-creds"),
	)

	res, err := Run(context.Background(), client, Options{
		Namespace: ns,
		Secret:    "db-creds",
		Data:      map[string][]byte{"password": []byte("new")},
		DryRun:    true,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Updated {
		t.Fatal("dry-run must not update")
	}
	if len(res.Impacts) != 1 {
		t.Fatalf("dry-run should still report impact, got %d", len(res.Impacts))
	}

	sec, _ := client.CoreV1().Secrets(ns).Get(context.Background(), "db-creds", metav1.GetOptions{})
	if string(sec.Data["password"]) != "old" {
		t.Fatal("dry-run changed the secret")
	}
	api, _ := client.AppsV1().Deployments(ns).Get(context.Background(), "api", metav1.GetOptions{})
	if _, ok := api.Spec.Template.Annotations[AnnotationKey]; ok {
		t.Fatal("dry-run stamped a workload")
	}
}

func TestKeepPreviousSnapshotsOldData(t *testing.T) {
	client := fake.NewSimpleClientset(
		secret("db-creds", map[string][]byte{"password": []byte("old")}),
	)

	_, err := Run(context.Background(), client, Options{
		Namespace:    ns,
		Secret:       "db-creds",
		Data:         map[string][]byte{"password": []byte("new")},
		KeepPrevious: true,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	prev, err := client.CoreV1().Secrets(ns).Get(context.Background(), "db-creds-prev", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("expected snapshot secret: %v", err)
	}
	if string(prev.Data["password"]) != "old" {
		t.Fatalf("snapshot should hold the pre-rotation value, got %q", prev.Data["password"])
	}
}

func TestRunMissingSecret(t *testing.T) {
	client := fake.NewSimpleClientset()
	_, err := Run(context.Background(), client, Options{Namespace: ns, Secret: "nope"})
	if err == nil {
		t.Fatal("expected error for missing secret")
	}
}
