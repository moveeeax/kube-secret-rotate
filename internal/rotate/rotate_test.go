package rotate

import (
	"context"
	"reflect"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
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

// envSpec is a pod spec whose only container reads the secret via env.
func envSpec(secretName string) corev1.PodSpec {
	return corev1.PodSpec{
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
	}
}

// projectedSpec mounts the secret through a projected volume.
func projectedSpec(secretName string) corev1.PodSpec {
	return corev1.PodSpec{
		Containers: []corev1.Container{{Name: "agent"}},
		Volumes: []corev1.Volume{{
			Name: "bundle",
			VolumeSource: corev1.VolumeSource{Projected: &corev1.ProjectedVolumeSource{
				Sources: []corev1.VolumeProjection{{
					Secret: &corev1.SecretProjection{LocalObjectReference: corev1.LocalObjectReference{Name: secretName}},
				}},
			}},
		}},
	}
}

func daemonSet(name string, spec corev1.PodSpec, strategy appsv1.DaemonSetUpdateStrategyType) *appsv1.DaemonSet {
	return &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: appsv1.DaemonSetSpec{
			UpdateStrategy: appsv1.DaemonSetUpdateStrategy{Type: strategy},
			Template:       corev1.PodTemplateSpec{Spec: spec},
		},
	}
}

func cronJob(name string, spec corev1.PodSpec) *batchv1.CronJob {
	return &batchv1.CronJob{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: batchv1.CronJobSpec{
			Schedule: "0 * * * *",
			JobTemplate: batchv1.JobTemplateSpec{
				Spec: batchv1.JobSpec{Template: corev1.PodTemplateSpec{Spec: spec}},
			},
		},
	}
}

func job(name string, spec corev1.PodSpec, conditions ...batchv1.JobCondition) *batchv1.Job {
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec:       batchv1.JobSpec{Template: corev1.PodTemplateSpec{Spec: spec}},
		Status:     batchv1.JobStatus{Conditions: conditions},
	}
}

func pod(name string, spec corev1.PodSpec, phase corev1.PodPhase, owners ...metav1.OwnerReference) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, OwnerReferences: owners},
		Spec:       spec,
		Status:     corev1.PodStatus{Phase: phase},
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

func TestRunCoversDaemonSetsCronJobsJobsAndPods(t *testing.T) {
	isController := true
	client := fake.NewSimpleClientset(
		secret("db-creds", map[string][]byte{"password": []byte("old")}),
		daemonSet("agent", projectedSpec("db-creds"), appsv1.RollingUpdateDaemonSetStrategyType),
		daemonSet("legacy-agent", envSpec("db-creds"), appsv1.OnDeleteDaemonSetStrategyType),
		daemonSet("other-agent", envSpec("other-secret"), appsv1.RollingUpdateDaemonSetStrategyType),
		cronJob("backup", envSpec("db-creds")),
		job("migrate", envSpec("db-creds")),
		job("migrate-done", envSpec("db-creds"),
			batchv1.JobCondition{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}),
		pod("debug", envSpec("db-creds"), corev1.PodRunning),
		pod("finished", envSpec("db-creds"), corev1.PodSucceeded),
		pod("migrate-x7k2p", envSpec("db-creds"), corev1.PodRunning, metav1.OwnerReference{
			APIVersion: "batch/v1", Kind: "Job", Name: "migrate", UID: "u1", Controller: &isController,
		}),
	)

	res, err := Run(context.Background(), client, Options{
		Namespace: ns,
		Secret:    "db-creds",
		Data:      map[string][]byte{"password": []byte("new")},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	want := []Impact{
		{Kind: "CronJob", Name: "backup", Reasons: []string{"env"}, Action: ActionNextRun},
		{Kind: "DaemonSet", Name: "agent", Reasons: []string{"projected"}, Action: ActionRoll},
		{Kind: "DaemonSet", Name: "legacy-agent", Reasons: []string{"env"}, Action: ActionOnDelete},
		{Kind: "Job", Name: "migrate", Reasons: []string{"env"}, Action: ActionManual},
		{Kind: "Pod", Name: "debug", Reasons: []string{"env"}, Action: ActionManual},
	}
	if !reflect.DeepEqual(res.Impacts, want) {
		t.Fatalf("impacts:\n got  %+v\n want %+v", res.Impacts, want)
	}

	ctx := context.Background()
	for _, name := range []string{"agent", "legacy-agent"} {
		ds, _ := client.AppsV1().DaemonSets(ns).Get(ctx, name, metav1.GetOptions{})
		if got := ds.Spec.Template.Annotations[AnnotationKey]; got != res.Checksum {
			t.Fatalf("daemonset %s not stamped: %q", name, got)
		}
	}
	other, _ := client.AppsV1().DaemonSets(ns).Get(ctx, "other-agent", metav1.GetOptions{})
	if _, ok := other.Spec.Template.Annotations[AnnotationKey]; ok {
		t.Fatal("other-agent should not have been stamped")
	}

	cj, _ := client.BatchV1().CronJobs(ns).Get(ctx, "backup", metav1.GetOptions{})
	if got := cj.Spec.JobTemplate.Spec.Template.Annotations[AnnotationKey]; got != res.Checksum {
		t.Fatalf("cronjob job template not stamped: %q", got)
	}

	// Jobs and Pods are reported only; their templates are left as they were.
	j, _ := client.BatchV1().Jobs(ns).Get(ctx, "migrate", metav1.GetOptions{})
	if _, ok := j.Spec.Template.Annotations[AnnotationKey]; ok {
		t.Fatal("running job must not be modified")
	}
	p, _ := client.CoreV1().Pods(ns).Get(ctx, "debug", metav1.GetOptions{})
	if _, ok := p.Annotations[AnnotationKey]; ok {
		t.Fatal("bare pod must not be modified")
	}
}

func TestStatefulSetOnDeleteIsReported(t *testing.T) {
	sts := statefulVolume("postgres", "db-creds")
	sts.Spec.UpdateStrategy.Type = appsv1.OnDeleteStatefulSetStrategyType
	client := fake.NewSimpleClientset(
		secret("db-creds", map[string][]byte{"password": []byte("old")}),
		sts,
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
	if len(res.Impacts) != 1 || res.Impacts[0].Action != ActionOnDelete {
		t.Fatalf("expected one OnDelete impact, got %+v", res.Impacts)
	}
}
