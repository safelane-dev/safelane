# cluster

Everything SafeLane needs on the target cluster, in one command.

Host prerequisites are Docker, Minikube, `kubectl`, Helm, Bash, and the
[Argo Rollouts kubectl plugin](https://argoproj.github.io/argo-rollouts/installation/#kubectl-plugin-installation)
at the version selected by `ARGO_ROLLOUTS_VERSION`. The installer checks the
plugin before it changes the cluster. The default demo uses
[Argo Rollouts v1.9.1](https://github.com/argoproj/argo-rollouts/releases/tag/v1.9.1).

```bash
./cluster/install.sh                        # safelane-demo-api (default)
SAFELANE_APP=<name> ./cluster/install.sh    # any app under cluster/apps/
```

From Windows PowerShell:

```powershell
& 'C:\Program Files\Git\bin\bash.exe' ./cluster/install.sh
```

Per-app data lives in `cluster/apps/<app>/`: an `app.env` plus the two
manifests that differ between applications. The scripts are generic; only the
data changes.

SafeLane itself does not provision clusters or render application manifests.
It verifies release evidence, validates an agent recommendation, changes only
the selected image and canary steps, and coordinates Argo. This folder is the
external, reproducible demo environment.

## Stages

| | Script | Leaves behind |
|---|---|---|
| 1 | `10-cluster.sh` | minikube running, `ingress-nginx`, Argo Rollouts (pinned) |
| 2 | `20-monitoring.sh` | Prometheus and Grafana via Helm |
| 3 | `30-baseline.sh` | the app at a healthy baseline digest, Services, and any AnalysisTemplate or Ingress |
| 4 | `40-loadgen.sh` | constant traffic, through the ingress or straight at the Service |
| 5 | `50-identities.sh` | `safelane-caller` and `safelane-controller` ServiceAccounts |

Every stage is idempotent and independently runnable. Identities run **last**:
that stage drops the default context to an identity that may only read
rollouts, so anything which still needs to create objects must precede it.

```bash
./cluster/reset.sh     # re-seed the baseline and clear release records
```

## Three things that are easy to get wrong

**Prometheus is not optional.** The AnalysisTemplate queries it, and an empty
result is deliberately treated as a *failed* reading rather than a healthy one.
A cluster without a metrics provider aborts every canary, blaming the change.

**The load generator is not optional either**, for the same reason: no traffic
means no numerator and no denominator.

**The scrape config must discover endpoints, not pods.** The analysis query
scopes on `service="<app>-canary"`, and only endpoint-role discovery knows
which Service currently selects a pod — Argo distinguishes canary from stable
by pointing each Service's *selector* at a pod-template-hash, never by
labelling the pod. Pod-role discovery scrapes perfectly happily and produces a
`service`-less series that silently never matches. See
`prometheus-values.yaml`.

## Identities

`50-identities.sh` rewrites your default kubeconfig context so the agent runs
as `safelane-caller-<app>`. It may read the selected Rollout and the referenced
Services and AnalysisTemplates, but it cannot mutate them. Your original
context is preserved as `safelane-admin`, and the kubeconfig is backed up with
a timestamp first.

The controller credential is separate and derived at
`~/.safelane/apps/<app>/environments/<environment>/identities/controller/kubeconfig`.
Its Role can get and patch only the named Rollout and read AnalysisRuns. The
controller context is not added to the ambient kubeconfig.

```bash
kubectl auth can-i patch rollouts -n safelane-demo-api                    # no
kubectl --context safelane-admin get pods -n safelane-demo-api            # ordinary work
```

That denial is enforced by Kubernetes, not by SafeLane — it holds even if
SafeLane is bypassed entirely.

## Configuration

| Variable | Default |
|---|---|
| `MINIKUBE_PROFILE` | `minikube` |
| `ARGO_ROLLOUTS_VERSION` | `v1.9.1` |
| `PROM_CHART_VERSION` | `29.23.0` |
| `GRAFANA_CHART_VERSION` | `10.5.15` |
| `SAFELANE_APP` | `safelane-demo-api` — selects `cluster/apps/<app>/` |
| `SAFELANE_ENVIRONMENT` | `production` — selects the derived identity path |

Pin Argo Rollouts and do not bump it casually: `ComputeStepHash` is not stable
across controller versions, and a change there can reset `currentStepIndex`
mid-rollout.

The application package owns its background AnalysisTemplate. SafeLane reads
that template and waits for fresh results; it never generates, patches, or
replaces the application's health definition.
