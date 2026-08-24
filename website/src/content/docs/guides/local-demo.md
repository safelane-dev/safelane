---
title: Run the Local Demo
description: Run SafeLane and Argo on a local cluster.
---

## Requirements

Install Docker, Minikube, `kubectl`, Helm, Bash, and the
[Argo Rollouts kubectl plugin](https://argoproj.github.io/argo-rollouts/installation/#kubectl-plugin-installation).

The plugin version must match `ARGO_ROLLOUTS_VERSION` in the demo scripts.
The default is [Argo Rollouts v1.9.1](https://github.com/argoproj/argo-rollouts/releases/tag/v1.9.1).

## Create the demo

From the SafeLane repository, run:

```bash
./cluster/install.sh
```

On Windows PowerShell, invoke Git Bash explicitly. Running `./install.sh` or
`.\install.sh` directly can open the file association instead of executing Bash:

```powershell
& 'C:\Program Files\Git\bin\bash.exe' ./cluster/install.sh
```

The script installs Argo Rollouts, Prometheus, Grafana, the demo application, traffic, and limited Kubernetes identities.

The caller can read the Rollout but cannot patch it:

```bash
kubectl auth can-i patch rollouts -n safelane-demo-api
# no
```

## Run a release

Open the demo application repository in Claude or Codex. Then ask:

```text
Register this application in production.
Deploy safelane-demo-api to production.
```

## Open the dashboards

```bash
kubectl argo rollouts dashboard
kubectl port-forward -n monitoring svc/grafana 3000:3000
```

Argo uses `http://localhost:3100`. Grafana uses `http://localhost:3000/d/safelane-rollout`.

Reset the demo with `./cluster/reset.sh`.
