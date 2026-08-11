package discover

import (
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
)

func envRef(secret string) corev1.EnvVar {
	return corev1.EnvVar{
		Name: "PW",
		ValueFrom: &corev1.EnvVarSource{
			SecretKeyRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: secret},
				Key:                  "password",
			},
		},
	}
}

func TestPodSpecRefs(t *testing.T) {
	tests := []struct {
		name   string
		spec   corev1.PodSpec
		secret string
		want   Refs
	}{
		{
			name:   "nil-ish empty spec",
			spec:   corev1.PodSpec{},
			secret: "db-creds",
			want:   Refs{},
		},
		{
			name: "env secretKeyRef match",
			spec: corev1.PodSpec{
				Containers: []corev1.Container{{Name: "app", Env: []corev1.EnvVar{envRef("db-creds")}}},
			},
			secret: "db-creds",
			want:   Refs{Env: true},
		},
		{
			name: "env ref to a different secret is ignored",
			spec: corev1.PodSpec{
				Containers: []corev1.Container{{Name: "app", Env: []corev1.EnvVar{envRef("other")}}},
			},
			secret: "db-creds",
			want:   Refs{},
		},
		{
			name: "envFrom secretRef match",
			spec: corev1.PodSpec{
				Containers: []corev1.Container{{
					Name:    "app",
					EnvFrom: []corev1.EnvFromSource{{SecretRef: &corev1.SecretEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "db-creds"}}}},
				}},
			},
			secret: "db-creds",
			want:   Refs{EnvFrom: true},
		},
		{
			name: "volume secret match",
			spec: corev1.PodSpec{
				Volumes: []corev1.Volume{{
					Name:         "certs",
					VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: "db-creds"}},
				}},
			},
			secret: "db-creds",
			want:   Refs{Volume: true},
		},
		{
			name: "init container env counts",
			spec: corev1.PodSpec{
				InitContainers: []corev1.Container{{Name: "migrate", Env: []corev1.EnvVar{envRef("db-creds")}}},
			},
			secret: "db-creds",
			want:   Refs{Env: true},
		},
		{
			name: "all three reference kinds at once",
			spec: corev1.PodSpec{
				Containers: []corev1.Container{{
					Name:    "app",
					Env:     []corev1.EnvVar{envRef("db-creds")},
					EnvFrom: []corev1.EnvFromSource{{SecretRef: &corev1.SecretEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "db-creds"}}}},
				}},
				Volumes: []corev1.Volume{{
					Name:         "certs",
					VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: "db-creds"}},
				}},
			},
			secret: "db-creds",
			want:   Refs{Env: true, EnvFrom: true, Volume: true},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := PodSpecRefs(&tt.spec, tt.secret)
			if got != tt.want {
				t.Fatalf("PodSpecRefs = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestPodSpecRefsNilSpec(t *testing.T) {
	if PodSpecRefs(nil, "db-creds").Any() {
		t.Fatal("nil spec should reference nothing")
	}
}

func TestReasons(t *testing.T) {
	got := Refs{Env: true, Volume: true}.Reasons()
	want := []string{"env", "volume"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Reasons = %v, want %v", got, want)
	}
	if len(Refs{}.Reasons()) != 0 {
		t.Fatal("empty Refs should have no reasons")
	}
}
