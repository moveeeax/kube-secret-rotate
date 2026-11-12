// Package discover inspects Kubernetes pod specs to find workloads that
// consume a given Secret. Detection is pure and deterministic so it can be
// unit-tested without a cluster.
package discover

import (
	"sort"

	corev1 "k8s.io/api/core/v1"
)

// Refs records the ways a pod template references a secret.
type Refs struct {
	Env       bool // container env[].valueFrom.secretKeyRef
	EnvFrom   bool // container envFrom[].secretRef
	Volume    bool // pod volumes[].secret.secretName
	Projected bool // pod volumes[].projected.sources[].secret.name
}

// Any reports whether the pod references the secret at all.
func (r Refs) Any() bool { return r.Env || r.EnvFrom || r.Volume || r.Projected }

// Reasons returns a stable, human-readable list of reference kinds.
func (r Refs) Reasons() []string {
	var out []string
	if r.Env {
		out = append(out, "env")
	}
	if r.EnvFrom {
		out = append(out, "envFrom")
	}
	if r.Volume {
		out = append(out, "volume")
	}
	if r.Projected {
		out = append(out, "projected")
	}
	sort.Strings(out)
	return out
}

// PodSpecRefs scans every container, init container, and volume of a pod spec
// for references to the named secret.
func PodSpecRefs(spec *corev1.PodSpec, secret string) Refs {
	var r Refs
	if spec == nil {
		return r
	}
	containers := make([]corev1.Container, 0, len(spec.Containers)+len(spec.InitContainers))
	containers = append(containers, spec.Containers...)
	containers = append(containers, spec.InitContainers...)

	for i := range containers {
		c := &containers[i]
		for _, e := range c.Env {
			if e.ValueFrom != nil && e.ValueFrom.SecretKeyRef != nil &&
				e.ValueFrom.SecretKeyRef.Name == secret {
				r.Env = true
			}
		}
		for _, ef := range c.EnvFrom {
			if ef.SecretRef != nil && ef.SecretRef.Name == secret {
				r.EnvFrom = true
			}
		}
	}
	for _, v := range spec.Volumes {
		if v.Secret != nil && v.Secret.SecretName == secret {
			r.Volume = true
		}
		if v.Projected != nil {
			for _, src := range v.Projected.Sources {
				if src.Secret != nil && src.Secret.Name == secret {
					r.Projected = true
				}
			}
		}
	}
	return r
}
