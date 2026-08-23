#!/usr/bin/env bash
# Return the cluster to empty: remove everything install.sh created, so that
# install.sh can run again from scratch and produce the same result.
#
# That round trip is the point. A demo you can only set up once is not
# reproducible, and "it worked yesterday" is not evidence.
#
# What goes:
#   <app>/           Rollout, both Services, the AnalysisTemplate, load
#                    generator, and the two SafeLane identities
#   monitoring/      Prometheus and Grafana
#   argo-rollouts/   the controller and its CRDs
#   ingress-nginx/   the minikube ingress addon
#   kubeconfig       the caller context, and the derived controller kubeconfig
#   release records  under the derived SafeLane path
#
# What stays: the minikube profile itself. 10-cluster.sh reuses an existing
# cluster on purpose -- the demo target is a cluster the infrastructure
# workstream owns, and deleting it is not this script's call.
#
# For a fast re-seed between rehearsals, run 30-baseline.sh instead: it
# recreates the Rollout at the baseline digest without tearing anything down.
set -euo pipefail

. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib.sh"
ADMIN_CONTEXT=safelane-admin
CALLER_CONTEXT="safelane-caller-${SAFELANE_APP}"
DEFAULT_KUBECONFIG="${HOME:?HOME is required}/.kube/config"
SAFELANE_CONFIG_HOME="${SAFELANE_HOME:-${HOME}/.safelane}"
ENV_HOME="${SAFELANE_CONFIG_HOME}/apps/${SAFELANE_APP}/environments/${SAFELANE_ENVIRONMENT}"
ARGO_VERSION="${ARGO_ROLLOUTS_VERSION:-v1.9.1}"
PROFILE="${MINIKUBE_PROFILE:-minikube}"

say() { printf '\n\033[1m==> %s\033[0m\n' "$1"; }

# Teardown needs write access; the caller identity deliberately has none.
if kubectl config get-contexts -o name | grep -Fxq "${ADMIN_CONTEXT}"; then
  kubectl config use-context "${ADMIN_CONTEXT}" >/dev/null
fi
if [ "$(kubectl auth can-i '*' '*' 2>/dev/null)" != "yes" ]; then
  echo "reset needs a cluster-admin context; current is $(kubectl config current-context)." >&2
  echo "Switch to one (kubectl config use-context ${ADMIN_CONTEXT}) and retry." >&2
  exit 1
fi

# This deletes CRDs, which deletes every Rollout in the cluster. Refuse on
# anything that is not a local demo cluster, for the same reason
# 50-identities.sh refuses to rewrite a kubeconfig pointing at one.
CLUSTER_NAME="$(kubectl config view -o "jsonpath={.contexts[?(@.name=='$(kubectl config current-context)')].context.cluster}")"
case "${CLUSTER_NAME}" in
  kind-*|minikube) ;;
  *)
    echo "Refusing to reset unrecognised cluster ${CLUSTER_NAME:-<empty>}." >&2
    echo "Expected a kind-* or minikube cluster." >&2
    exit 1
    ;;
esac

say "application ${SAFELANE_APP}"
kubectl delete namespace "${NAMESPACE}" --ignore-not-found --wait=true

say "monitoring"
helm uninstall grafana -n monitoring >/dev/null 2>&1 || true
helm uninstall prometheus -n monitoring >/dev/null 2>&1 || true
kubectl delete namespace monitoring --ignore-not-found --wait=true

say "Argo Rollouts"
kubectl delete -n argo-rollouts \
  -f "https://github.com/argoproj/argo-rollouts/releases/download/${ARGO_VERSION}/install.yaml" \
  --ignore-not-found --wait=true >/dev/null 2>&1 || true
kubectl delete namespace argo-rollouts --ignore-not-found --wait=true

say "ingress-nginx"
minikube addons disable ingress -p "${PROFILE}" >/dev/null 2>&1 || true
kubectl delete namespace ingress-nginx --ignore-not-found --wait=true

say "identities"
# The admin context stays: install.sh needs a cluster-admin context to start
# from, and 50-identities.sh reuses this one rather than renaming a second time.
kubectl --kubeconfig "${DEFAULT_KUBECONFIG}" config delete-context "${CALLER_CONTEXT}" >/dev/null 2>&1 || true
kubectl --kubeconfig "${DEFAULT_KUBECONFIG}" config unset "users.${CALLER_CONTEXT}" >/dev/null 2>&1 || true
rm -f "${ENV_HOME}/identities/controller/kubeconfig"

say "release records"
if [ -d "${ENV_HOME}/releases" ]; then
  count=$(find "${ENV_HOME}/releases" -name '*.json' | wc -l)
  find "${ENV_HOME}/releases" -name '*.json' -delete
  echo "cleared ${count} release record(s) from ${ENV_HOME}/releases"
else
  echo "no release records at ${ENV_HOME}/releases"
fi

cat <<DONE

######## cluster empty ########

Current context is $(kubectl config current-context).

  ./cluster/install.sh     # bring it all back
DONE
