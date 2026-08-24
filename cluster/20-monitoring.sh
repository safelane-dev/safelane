#!/usr/bin/env bash
# Prometheus and Grafana, via Helm.
#
# Prometheus is not optional: the AnalysisTemplate queries it, and an empty
# result is treated as a failed reading, so a rollout without a metrics
# provider aborts every time for the wrong reason.
#
# Deliberately NOT kube-prometheus-stack. That chart brings the Operator,
# Alertmanager, node-exporter and kube-state-metrics (~2 GiB), defaults to a
# 30s scrape when this needs 5s, and moves scrape config into PodMonitor CRDs,
# which would mean rewriting the relabel rule everything depends on.
set -euo pipefail

. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib.sh"
HERE="${CLUSTER_DIR}"
PROM_CHART_VERSION="${PROM_CHART_VERSION:-29.23.0}"
GRAFANA_CHART_VERSION="${GRAFANA_CHART_VERSION:-10.5.15}"

say() { printf '\n\033[1m==> %s\033[0m\n' "$1"; }

say "helm repositories"
helm repo add prometheus-community https://prometheus-community.github.io/helm-charts >/dev/null 2>&1 || true
helm repo add grafana https://grafana.github.io/helm-charts >/dev/null 2>&1 || true
helm repo update >/dev/null

kubectl create namespace monitoring --dry-run=client -o yaml | kubectl apply -f -

say "Prometheus"
# server.fullnameOverride pins the Service to prometheus.monitoring.svc:9090,
# which is the address baked into the rendered AnalysisTemplate.
PROM_VALUES="$(mktemp "${HERE}/.prometheus-values.XXXXXX.yaml")"
DASHBOARD="$(mktemp "${HERE}/.grafana-dashboard.XXXXXX.yaml")"
cleanup() {
  rm -f -- "${PROM_VALUES}" "${DASHBOARD}"
}
trap cleanup EXIT

render "${HERE}/prometheus-values.yaml" > "${PROM_VALUES}"
[ -s "${PROM_VALUES}" ] || { echo "Prometheus values rendered empty" >&2; exit 1; }
helm upgrade --install prometheus prometheus-community/prometheus \
  --version "${PROM_CHART_VERSION}" -n monitoring \
  -f "${PROM_VALUES}" --wait --timeout 8m

say "Grafana"
# Applied before the chart: it mounts this ConfigMap, and a missing one leaves
# the pod in ContainerCreating.
render "${HERE}/grafana-dashboard.yaml" > "${DASHBOARD}"
[ -s "${DASHBOARD}" ] || { echo "dashboard rendered empty" >&2; exit 1; }
kubectl apply -f "${DASHBOARD}"
helm upgrade --install grafana grafana/grafana \
  --version "${GRAFANA_CHART_VERSION}" -n monitoring \
  -f "${HERE}/grafana-values.yaml" --wait --timeout 8m

say "monitoring ready"
kubectl get pods -n monitoring
