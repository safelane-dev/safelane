#!/usr/bin/env bash
# Cluster prerequisites: a running minikube, an ingress controller, and the
# Argo Rollouts controller.
#
# Idempotent. Reuses an existing minikube rather than creating its own cluster,
# because the demo target is a cluster the infrastructure workstream owns.
set -euo pipefail

PROFILE="${MINIKUBE_PROFILE:-minikube}"
ARGO_VERSION="${ARGO_ROLLOUTS_VERSION:-v1.9.1}"

say() { printf '\n\033[1m==> %s\033[0m\n' "$1"; }

say "minikube (profile ${PROFILE})"
if ! minikube status -p "${PROFILE}" --format '{{.APIServer}}' 2>/dev/null | grep -q Running; then
  minikube start -p "${PROFILE}" --cpus=4 --memory=4096
else
  echo "already running"
fi

# Minikube creates a profile-named context on a first run. Identity setup later
# renames that context to safelane-admin, so an idempotent re-run must preserve
# the renamed administrator instead of trying to switch back to a name that no
# longer exists.
if kubectl config get-contexts -o name | grep -Fxq safelane-admin; then
  kubectl config use-context safelane-admin >/dev/null
else
  kubectl config use-context "${PROFILE}" >/dev/null
fi
if [ "$(kubectl auth can-i '*' '*' 2>/dev/null)" != "yes" ]; then
  echo "the selected minikube context is not cluster-admin; refusing demo setup" >&2
  exit 1
fi

say "ingress-nginx"
# Required: the Rollout uses trafficRouting.nginx, so weights are a real
# traffic split rather than an approximation by replica count.
if kubectl get ingressclass nginx >/dev/null 2>&1; then
  echo "ingressclass nginx already present"
else
  minikube addons enable ingress -p "${PROFILE}"
fi
kubectl rollout status -n ingress-nginx deploy/ingress-nginx-controller --timeout=300s

say "Argo Rollouts ${ARGO_VERSION}"
kubectl create namespace argo-rollouts --dry-run=client -o yaml | kubectl apply -f -
# install.yaml INCLUDES the CRDs; namespace-install.yaml does not, and using
# that one gives 'no matches for kind "Rollout"' much later and confusingly.
kubectl apply -n argo-rollouts \
  -f "https://github.com/argoproj/argo-rollouts/releases/download/${ARGO_VERSION}/install.yaml"
kubectl rollout status -n argo-rollouts deploy/argo-rollouts --timeout=300s

say "cluster ready"
kubectl get ingressclass
kubectl get deploy -n argo-rollouts
