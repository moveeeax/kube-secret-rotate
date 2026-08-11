// Package rotate updates a Kubernetes Secret and rolls the workloads that
// consume it. It works against any kubernetes.Interface, so the same code path
// runs against a real cluster and against a fake clientset in tests.
package rotate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/moveeeax/kube-secret-rotate/internal/discover"
)

// AnnotationKey is stamped onto a workload's pod template. Changing its value
// changes the pod template hash, which is what makes the Deployment or
// StatefulSet controller roll its pods.
const AnnotationKey = "kube-secret-rotate/checksum"

// Options configures a rotation.
type Options struct {
	Namespace string
	Secret    string
	// Data replaces the named keys in the Secret. Keys absent from Data are
	// left untouched.
	Data map[string][]byte
	// DryRun reports the impact without mutating anything.
	DryRun bool
	// KeepPrevious snapshots the current Secret to "<secret>-prev" before
	// updating, so a rollback has something to restore from.
	KeepPrevious bool
}

// Impact is a single workload affected by a rotation.
type Impact struct {
	Kind    string // "Deployment" or "StatefulSet"
	Name    string
	Reasons []string
}

// Result summarizes a rotation.
type Result struct {
	Checksum     string
	Updated      bool
	KeptPrevious bool
	Impacts      []Impact
}

// Checksum returns a stable hex digest of secret data, independent of map
// iteration order.
func Checksum(data map[string][]byte) string {
	keys := make([]string, 0, len(data))
	for k := range data {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := sha256.New()
	for _, k := range keys {
		h.Write([]byte(k))
		h.Write([]byte{0})
		h.Write(data[k])
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Run executes a rotation according to opts. On dry-run it fills Impacts and
// Checksum but performs no writes.
func Run(ctx context.Context, client kubernetes.Interface, opts Options) (*Result, error) {
	secrets := client.CoreV1().Secrets(opts.Namespace)
	sec, err := secrets.Get(ctx, opts.Secret, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("secret %q not found in namespace %q", opts.Secret, opts.Namespace)
		}
		return nil, fmt.Errorf("get secret: %w", err)
	}

	next := map[string][]byte{}
	for k, v := range sec.Data {
		next[k] = v
	}
	for k, v := range opts.Data {
		next[k] = v
	}
	checksum := Checksum(next)

	impacts, err := discoverImpacts(ctx, client, opts.Namespace, opts.Secret)
	if err != nil {
		return nil, err
	}

	res := &Result{Checksum: checksum, Impacts: impacts}
	if opts.DryRun {
		return res, nil
	}

	if opts.KeepPrevious {
		if err := snapshot(ctx, secrets, sec); err != nil {
			return nil, err
		}
		res.KeptPrevious = true
	}

	sec.Data = next
	if _, err := secrets.Update(ctx, sec, metav1.UpdateOptions{}); err != nil {
		return nil, fmt.Errorf("update secret: %w", err)
	}
	res.Updated = true

	if err := roll(ctx, client, opts.Namespace, impacts, checksum); err != nil {
		return nil, err
	}
	return res, nil
}

func snapshot(ctx context.Context, secrets secretWriter, sec *corev1.Secret) error {
	prevName := sec.Name + "-prev"
	prev := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: prevName, Namespace: sec.Namespace},
		Type:       sec.Type,
		Data:       sec.Data,
	}
	_, err := secrets.Create(ctx, prev, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		existing, gerr := secrets.Get(ctx, prevName, metav1.GetOptions{})
		if gerr != nil {
			return fmt.Errorf("refresh previous snapshot: %w", gerr)
		}
		existing.Data = sec.Data
		existing.Type = sec.Type
		if _, uerr := secrets.Update(ctx, existing, metav1.UpdateOptions{}); uerr != nil {
			return fmt.Errorf("update previous snapshot: %w", uerr)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("create previous snapshot: %w", err)
	}
	return nil
}

// secretWriter is the slice of the Secrets client that snapshot needs; keeping
// it small makes the intent obvious.
type secretWriter interface {
	Get(ctx context.Context, name string, opts metav1.GetOptions) (*corev1.Secret, error)
	Create(ctx context.Context, s *corev1.Secret, opts metav1.CreateOptions) (*corev1.Secret, error)
	Update(ctx context.Context, s *corev1.Secret, opts metav1.UpdateOptions) (*corev1.Secret, error)
}

func discoverImpacts(ctx context.Context, client kubernetes.Interface, ns, secret string) ([]Impact, error) {
	var impacts []Impact

	deploys, err := client.AppsV1().Deployments(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list deployments: %w", err)
	}
	for i := range deploys.Items {
		d := &deploys.Items[i]
		if refs := discover.PodSpecRefs(&d.Spec.Template.Spec, secret); refs.Any() {
			impacts = append(impacts, Impact{Kind: "Deployment", Name: d.Name, Reasons: refs.Reasons()})
		}
	}

	sets, err := client.AppsV1().StatefulSets(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list statefulsets: %w", err)
	}
	for i := range sets.Items {
		s := &sets.Items[i]
		if refs := discover.PodSpecRefs(&s.Spec.Template.Spec, secret); refs.Any() {
			impacts = append(impacts, Impact{Kind: "StatefulSet", Name: s.Name, Reasons: refs.Reasons()})
		}
	}

	sort.Slice(impacts, func(i, j int) bool {
		if impacts[i].Kind != impacts[j].Kind {
			return impacts[i].Kind < impacts[j].Kind
		}
		return impacts[i].Name < impacts[j].Name
	})
	return impacts, nil
}

func roll(ctx context.Context, client kubernetes.Interface, ns string, impacts []Impact, checksum string) error {
	for _, im := range impacts {
		switch im.Kind {
		case "Deployment":
			d, err := client.AppsV1().Deployments(ns).Get(ctx, im.Name, metav1.GetOptions{})
			if err != nil {
				return fmt.Errorf("get deployment %s: %w", im.Name, err)
			}
			stamp(&d.Spec.Template, checksum)
			if _, err := client.AppsV1().Deployments(ns).Update(ctx, d, metav1.UpdateOptions{}); err != nil {
				return fmt.Errorf("roll deployment %s: %w", im.Name, err)
			}
		case "StatefulSet":
			s, err := client.AppsV1().StatefulSets(ns).Get(ctx, im.Name, metav1.GetOptions{})
			if err != nil {
				return fmt.Errorf("get statefulset %s: %w", im.Name, err)
			}
			stamp(&s.Spec.Template, checksum)
			if _, err := client.AppsV1().StatefulSets(ns).Update(ctx, s, metav1.UpdateOptions{}); err != nil {
				return fmt.Errorf("roll statefulset %s: %w", im.Name, err)
			}
		}
	}
	return nil
}

func stamp(tmpl *corev1.PodTemplateSpec, checksum string) {
	if tmpl.Annotations == nil {
		tmpl.Annotations = map[string]string{}
	}
	tmpl.Annotations[AnnotationKey] = checksum
}
