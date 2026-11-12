// Command kube-secret-rotate updates a Kubernetes Secret and rolls the
// Deployments, StatefulSets and DaemonSets that consume it. CronJobs get the
// new value on their next run; running Jobs and bare Pods are reported for a
// manual restart.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/moveeeax/kube-secret-rotate/internal/rotate"
)

type stringList []string

func (s *stringList) String() string { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error {
	*s = append(*s, v)
	return nil
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("kube-secret-rotate", flag.ContinueOnError)
	var (
		secret     = fs.String("secret", "", "name of the Secret to rotate (required)")
		namespace  = fs.String("n", "default", "namespace of the Secret")
		kubeconfig = fs.String("kubeconfig", defaultKubeconfig(), "path to kubeconfig")
		dryRun     = fs.Bool("dry-run", false, "print impacted workloads without changing anything")
		keepPrev   = fs.Bool("keep-previous", false, "snapshot the current Secret to <secret>-prev before updating")
		fromFiles  stringList
		fromLits   stringList
	)
	fs.Var(&fromFiles, "from-file", "key=path (or path); load Secret value(s) from a file, repeatable")
	fs.Var(&fromLits, "from-literal", "key=value literal, repeatable")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *secret == "" {
		return fmt.Errorf("--secret is required")
	}

	data, err := collectData(fromFiles, fromLits)
	if err != nil {
		return err
	}
	if len(data) == 0 && !*dryRun {
		return fmt.Errorf("nothing to rotate: pass --from-file or --from-literal (or use --dry-run)")
	}

	client, err := newClient(*kubeconfig)
	if err != nil {
		return err
	}

	res, err := rotate.Run(context.Background(), client, rotate.Options{
		Namespace:    *namespace,
		Secret:       *secret,
		Data:         data,
		DryRun:       *dryRun,
		KeepPrevious: *keepPrev,
	})
	if err != nil {
		return err
	}
	report(os.Stdout, *secret, *namespace, *dryRun, res)
	return nil
}

func collectData(files, literals []string) (map[string][]byte, error) {
	data := map[string][]byte{}
	for _, f := range files {
		key, path := f, f
		if i := strings.Index(f, "="); i >= 0 {
			key, path = f[:i], f[i+1:]
		} else {
			key = filepath.Base(f)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read --from-file %q: %w", path, err)
		}
		data[key] = b
	}
	for _, l := range literals {
		i := strings.Index(l, "=")
		if i < 0 {
			return nil, fmt.Errorf("--from-literal %q must be key=value", l)
		}
		data[l[:i]] = []byte(l[i+1:])
	}
	return data, nil
}

func report(w io.Writer, secret, ns string, dryRun bool, res *rotate.Result) {
	verb := "Rotated"
	if dryRun {
		verb = "Would rotate"
	}
	fmt.Fprintf(w, "%s secret %s/%s (checksum %s)\n", verb, ns, secret, res.Checksum[:12])
	if res.KeptPrevious {
		fmt.Fprintf(w, "  saved previous value to %s-prev\n", secret)
	}
	if len(res.Impacts) == 0 {
		fmt.Fprintln(w, "  no workloads reference this secret")
		return
	}

	byAction := map[rotate.Action][]rotate.Impact{}
	for _, im := range res.Impacts {
		byAction[im.Action] = append(byAction[im.Action], im)
	}
	// Each group's header is a format string taking the workload count.
	groups := []struct {
		action    rotate.Action
		live, dry string
	}{
		{rotate.ActionRoll,
			"rolling %d workload(s):",
			"would roll %d workload(s):"},
		{rotate.ActionOnDelete,
			"stamped %d workload(s) with OnDelete strategy, delete their pods to restart:",
			"would stamp %d workload(s) with OnDelete strategy, delete their pods to restart:"},
		{rotate.ActionNextRun,
			"stamped %d CronJob(s), next run picks it up, no restart:",
			"would stamp %d CronJob(s), next run picks it up, no restart:"},
		{rotate.ActionManual,
			"%d unmanaged workload(s), restart by hand:",
			"%d unmanaged workload(s), restart by hand:"},
	}
	for _, g := range groups {
		list := byAction[g.action]
		if len(list) == 0 {
			continue
		}
		format := g.live
		if dryRun {
			format = g.dry
		}
		fmt.Fprintf(w, "  "+format+"\n", len(list))
		for _, im := range list {
			fmt.Fprintf(w, "    - %s/%s (%s)\n", im.Kind, im.Name, strings.Join(im.Reasons, ","))
		}
	}
}

func newClient(kubeconfig string) (kubernetes.Interface, error) {
	loading := clientcmd.NewDefaultClientConfigLoadingRules()
	if kubeconfig != "" {
		loading.ExplicitPath = kubeconfig
	}
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		loading, &clientcmd.ConfigOverrides{}).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("load kubeconfig: %w", err)
	}
	return kubernetes.NewForConfig(cfg)
}

func defaultKubeconfig() string {
	if v := os.Getenv("KUBECONFIG"); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".kube", "config")
}
