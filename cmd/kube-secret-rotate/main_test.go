package main

import (
	"strings"
	"testing"

	"github.com/moveeeax/kube-secret-rotate/internal/rotate"
)

func TestReportGroupsByAction(t *testing.T) {
	res := &rotate.Result{
		Checksum: strings.Repeat("a", 64),
		Impacts: []rotate.Impact{
			{Kind: "CronJob", Name: "backup", Reasons: []string{"env"}, Action: rotate.ActionNextRun},
			{Kind: "DaemonSet", Name: "agent", Reasons: []string{"projected"}, Action: rotate.ActionRoll},
			{Kind: "DaemonSet", Name: "legacy", Reasons: []string{"env"}, Action: rotate.ActionOnDelete},
			{Kind: "Deployment", Name: "api", Reasons: []string{"env"}, Action: rotate.ActionRoll},
			{Kind: "Job", Name: "migrate", Reasons: []string{"env"}, Action: rotate.ActionManual},
			{Kind: "Pod", Name: "debug", Reasons: []string{"volume"}, Action: rotate.ActionManual},
		},
	}

	var b strings.Builder
	report(&b, "db-creds", "prod", true, res)

	want := `Would rotate secret prod/db-creds (checksum aaaaaaaaaaaa)
  would roll 2 workload(s):
    - DaemonSet/agent (projected)
    - Deployment/api (env)
  would stamp 1 workload(s) with OnDelete strategy, delete their pods to restart:
    - DaemonSet/legacy (env)
  would stamp 1 CronJob(s), next run picks it up, no restart:
    - CronJob/backup (env)
  2 unmanaged workload(s), restart by hand:
    - Job/migrate (env)
    - Pod/debug (volume)
`
	if got := b.String(); got != want {
		t.Fatalf("report:\n%s\nwant:\n%s", got, want)
	}
}

func TestReportNoImpacts(t *testing.T) {
	var b strings.Builder
	report(&b, "db-creds", "prod", false, &rotate.Result{Checksum: strings.Repeat("b", 64)})
	if !strings.Contains(b.String(), "no workloads reference this secret") {
		t.Fatalf("unexpected report: %q", b.String())
	}
}
