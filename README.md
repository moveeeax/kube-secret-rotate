# kube-secret-rotate

[![ci](https://github.com/moveeeax/kube-secret-rotate/actions/workflows/ci.yml/badge.svg)](https://github.com/moveeeax/kube-secret-rotate/actions/workflows/ci.yml)
[![Go](https://img.shields.io/badge/go-1.26%20%7C%201.27-00ADD8?logo=go&logoColor=white)](https://go.dev)
[![License: MIT](https://img.shields.io/badge/license-MIT-green.svg)](LICENSE)

One command to rotate a Kubernetes `Secret` **and** roll the workloads that
actually consume it — nothing more, nothing less.

Kubernetes will happily let you `kubectl edit secret db-creds` and change a
password. What it will *not* do is restart the pods that mounted the old value.
They keep running with stale credentials until something evicts them. This tool
closes that gap: it updates the Secret, finds every Deployment, StatefulSet,
DaemonSet and CronJob that references it (via `env`, `envFrom`, a secret volume
or a projected volume), and stamps a checksum annotation on their pod templates
so the controllers perform a rolling restart — only for the workloads that are
genuinely impacted. Running Jobs and bare Pods that use the Secret are listed so
you can restart them by hand.

## How it works

1. **Update** the `Secret` from `--from-file` / `--from-literal` (only the keys
   you pass are changed; the rest are left as-is).
2. **Discover** consumers by scanning every Deployment, StatefulSet, DaemonSet,
   CronJob, Job and Pod in the namespace for references to the Secret:
   - `env[].valueFrom.secretKeyRef` (reason `env`)
   - `envFrom[].secretRef` (reason `envFrom`)
   - `volumes[].secret.secretName` (reason `volume`)
   - `volumes[].projected.sources[].secret.name` (reason `projected`)
   (init containers included).
3. **Roll** each impacted workload by writing a content checksum to the pod
   template annotation `kube-secret-rotate/checksum`. What that does depends on
   the kind:

   | Kind | What happens |
   |------|--------------|
   | Deployment, StatefulSet, DaemonSet | `spec.template` is stamped; the controller does a normal rolling update. Untouched workloads are never modified. |
   | StatefulSet, DaemonSet with `updateStrategy: OnDelete` | `spec.template` is stamped, but the controller does not restart pods on its own. Reported as "delete their pods to restart". |
   | CronJob | `spec.jobTemplate.spec.template` is stamped. Nothing restarts; the next scheduled Job runs with the new value. |
   | Job (not finished) | Reported as unmanaged, not modified: a Job's pod template is immutable. Restart by hand. |
   | Pod without a controller owner (Pending or Running) | Reported as unmanaged, not modified. Restart by hand. |

   Pods that have a controller owner (a ReplicaSet, a Job, an operator) are not
   listed on their own; their owner is.

`--dry-run` prints the impact and changes nothing. `--keep-previous` snapshots
the current Secret to `<secret>-prev` first, so a rollback has something to
restore from.

## Install

```shell
go install github.com/moveeeax/kube-secret-rotate/cmd/kube-secret-rotate@latest
```

## Usage

```shell
# Rotate db-creds from a new file and roll everything that uses it
kube-secret-rotate --secret db-creds -n prod --from-file password=./new.env

# See what would happen, touch nothing
kube-secret-rotate --secret db-creds -n prod --from-literal password=hunter2 --dry-run
```

Dry-run against the sample workloads in `examples/`:

```text
$ kube-secret-rotate --secret db-creds -n demo --from-literal password=new --dry-run
Would rotate secret demo/db-creds (checksum a1b2c3d4e5f6)
  would roll 3 workload(s):
    - DaemonSet/node-agent (projected)
    - Deployment/api (env)
    - StatefulSet/postgres (volume)
  would stamp 1 CronJob(s), next run picks it up, no restart:
    - CronJob/nightly-backup (envFrom)
```

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--secret` | — | Secret to rotate (required) |
| `-n` | `default` | Namespace |
| `--from-file` | — | `key=path` (or `path`); repeatable |
| `--from-literal` | — | `key=value`; repeatable |
| `--dry-run` | `false` | Report impact, change nothing |
| `--keep-previous` | `false` | Snapshot to `<secret>-prev` before updating |
| `--kubeconfig` | `$KUBECONFIG` or `~/.kube/config` | kubeconfig path |

### Permissions

Discovery lists `deployments`, `statefulsets`, `daemonsets` (apps), `cronjobs`,
`jobs` (batch) and `pods` in the namespace. Rolling needs `get` and `update` on
the first four. The Secret itself needs `get` and `update` (plus `create` with
`--keep-previous`). If any list call is forbidden, the run fails before changing
anything rather than reporting a partial impact.

## Try it end-to-end

```shell
kubectl apply -f examples/workloads.yaml
kube-secret-rotate --secret db-creds -n demo --from-file password=examples/new.env
kubectl -n demo rollout status deploy/api
```

## Development

```shell
go test -race ./...
go build ./cmd/kube-secret-rotate
```

The discovery and rotation logic run against a fake clientset in tests, so the
whole flow is verified without a cluster.

## License

MIT — see [LICENSE](LICENSE).
