# kube-secret-rotate

> One command to rotate a Secret and roll the pods that use it.

**Status:** 🚧 In development

## Overview

Rotate a Kubernetes Secret and trigger a rolling restart of dependent workloads.

## Features

- Updates Secret from a new value/file/generator
- Finds Deployments/StatefulSets referencing it
- Triggers rollout restart with checksum annotation
- Dry-run showing impacted workloads
- Optional keep-previous for rollback

## Stack

Go + client-go.

## Usage

```bash
kube-secret-rotate --secret db-creds -n prod --from-file ./new.env
```

## License

MIT
