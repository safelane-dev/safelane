#!/usr/bin/env bash
# Mirrors SafeLane's two identities in the application namespace.
#
# Neither identity may create anything. The application package owns the
# Rollout, both Services and the background AnalysisTemplate; SafeLane reads
# them and patches two fields of the Rollout. The RBAC below is the boundary
# that makes "SafeLane cannot take cluster ownership back" a property of
# Kubernetes rather than a promise in a README.
#
# Both paths derive from SAFELANE_APP and SAFELANE_ENVIRONMENT. No SafeLane
# configuration file is read, because this stage runs before SafeLane has been
# configured at all.
#
# Safe to run twice: RBAC and token Secrets are declarative, the preserved
# admin context is reused, and both generated ServiceAccount kubeconfigs are
# replaced atomically. The caller becomes the default context; the controller
# context is written only under the derived controller identity path.
set -euo pipefail

. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib.sh"
CALLER=safelane-caller
# The ServiceAccount name is the same in every namespace, but the caller's
# kubectl context lives in the SHARED default kubeconfig -- so its name must be
# app-scoped. Without this, provisioning a second application silently rebinds
# `safelane-caller` to the new namespace and the first app's context is gone.
CALLER_CONTEXT="safelane-caller-${SAFELANE_APP}"
CONTROLLER=safelane-controller
ADMIN_CONTEXT=safelane-admin
DEFAULT_KUBECONFIG="${HOME:?HOME is required}/.kube/config"
SAFELANE_CONFIG_HOME="${SAFELANE_HOME:-${HOME}/.safelane}"
# The one derived location. SafeLane resolves the same path from the same two
# names; nothing writes it into YAML and nothing reads it back out.
CONTROLLER_KUBECONFIG="${SAFELANE_CONFIG_HOME}/apps/${SAFELANE_APP}/environments/${SAFELANE_ENVIRONMENT}/identities/controller/kubeconfig"

context_exists() {
  kubectl --kubeconfig "${DEFAULT_KUBECONFIG}" config get-contexts -o name |
    grep -Fxq -- "$1"
}

if [[ ! -f "${DEFAULT_KUBECONFIG}" ]]; then
  echo "Refusing to continue: ${DEFAULT_KUBECONFIG} is not a regular file." >&2
  exit 1
fi
if [[ -L "${DEFAULT_KUBECONFIG}" ]]; then
  echo "Refusing to mutate symlinked kubeconfig ${DEFAULT_KUBECONFIG}." >&2
  exit 1
fi
mkdir -p "$(dirname "${CONTROLLER_KUBECONFIG}")"

BACKUP="${DEFAULT_KUBECONFIG}.safelane-backup.$(date -u +%Y%m%dT%H%M%SZ)"
suffix=0
while [[ -e "${BACKUP}" ]]; do
  suffix=$((suffix + 1))
  BACKUP="${DEFAULT_KUBECONFIG}.safelane-backup.$(date -u +%Y%m%dT%H%M%SZ).${suffix}"
done
cp -p "${DEFAULT_KUBECONFIG}" "${BACKUP}"
echo "Backed up kubeconfig to ${BACKUP}"

if context_exists "${ADMIN_CONTEXT}"; then
  : # Re-run: the original kind administrator was already preserved.
else
  CURRENT_CONTEXT="$(kubectl --kubeconfig "${DEFAULT_KUBECONFIG}" config current-context)"
  case "${CURRENT_CONTEXT}" in
    ""|"${CALLER}"|safelane-caller-*|"${CONTROLLER}")
      echo "No recoverable administrator context exists; restore ${BACKUP} and retry." >&2
      exit 1
      ;;
  esac
  kubectl --kubeconfig "${DEFAULT_KUBECONFIG}" config rename-context \
    "${CURRENT_CONTEXT}" "${ADMIN_CONTEXT}" >/dev/null
fi

CLUSTER_NAME="$(kubectl --kubeconfig "${DEFAULT_KUBECONFIG}" config view \
  -o "jsonpath={.contexts[?(@.name=='${ADMIN_CONTEXT}')].context.cluster}")"
# kind is the local development cluster; minikube is Ahmed's demo target
# (PLAN.md V2 names it as the preferred venue). Anything else is very likely a
# real cluster somebody's work depends on, and this script rewrites the default
# kubeconfig context -- so refuse rather than guess.
case "${CLUSTER_NAME}" in
  kind-*|minikube) ;;
  *)
    echo "Refusing unrecognised admin context ${ADMIN_CONTEXT} (cluster ${CLUSTER_NAME:-<empty>})." >&2
    echo "Expected a kind-* or minikube cluster." >&2
    exit 1
    ;;
esac
if [[ "$(kubectl --kubeconfig "${DEFAULT_KUBECONFIG}" --context "${ADMIN_CONTEXT}" auth can-i '*' '*')" != "yes" ]]; then
  echo "Context ${ADMIN_CONTEXT} is not cluster-admin; restore ${BACKUP} and retry." >&2
  exit 1
fi

kubectl --kubeconfig "${DEFAULT_KUBECONFIG}" --context "${ADMIN_CONTEXT}" apply -f - <<EOF
apiVersion: v1
kind: ServiceAccount
metadata:
  name: ${CALLER}
  namespace: ${NAMESPACE}
---
apiVersion: v1
kind: Secret
metadata:
  name: ${CALLER}-token
  namespace: ${NAMESPACE}
  annotations:
    kubernetes.io/service-account.name: ${CALLER}
type: kubernetes.io/service-account-token
---
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: ${CALLER}
  namespace: ${NAMESPACE}
rules:
  # Everything the caller does is discovery and status: find the Rollout, read
  # which Services and background AnalysisTemplate it references, resolve them.
  - apiGroups: ["argoproj.io"]
    resources: ["rollouts"]
    verbs: ["get", "list", "watch"]
  - apiGroups: [""]
    resources: ["services"]
    verbs: ["get", "list"]
  - apiGroups: ["argoproj.io"]
    resources: ["analysistemplates"]
    verbs: ["get", "list"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: ${CALLER}
  namespace: ${NAMESPACE}
subjects:
  - kind: ServiceAccount
    name: ${CALLER}
    namespace: ${NAMESPACE}
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: ${CALLER}
---
apiVersion: v1
kind: ServiceAccount
metadata:
  name: ${CONTROLLER}
  namespace: ${NAMESPACE}
---
apiVersion: v1
kind: Secret
metadata:
  name: ${CONTROLLER}-token
  namespace: ${NAMESPACE}
  annotations:
    kubernetes.io/service-account.name: ${CONTROLLER}
type: kubernetes.io/service-account-token
---
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: ${CONTROLLER}
  namespace: ${NAMESPACE}
rules:
  # One object, two verbs. resourceNames pins this to the Rollout the
  # application package installed, so the privileged identity cannot touch a
  # second Rollout that happens to share the namespace. The status subresource
  # is what the kubectl Argo promote and abort commands write; without it the
  # natural controls fail at the boundary rather than at the decision.
  - apiGroups: ["argoproj.io"]
    resources: ["rollouts", "rollouts/status"]
    resourceNames: ["${ROLLOUT}"]
    verbs: ["get", "patch"]
  # Read-only, and the only thing here that is not the Rollout: the AnalysisRun
  # names are chosen by Argo at run time, so they cannot be named in advance.
  - apiGroups: ["argoproj.io"]
    resources: ["analysisruns"]
    verbs: ["get"]
  # No Services, AnalysisTemplates or Ingresses, and no create or update
  # anywhere. The application owns those; SafeLane no longer renders them, so a
  # regression that starts rendering them again fails here instead of silently
  # taking ownership back.
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: ${CONTROLLER}
  namespace: ${NAMESPACE}
subjects:
  - kind: ServiceAccount
    name: ${CONTROLLER}
    namespace: ${NAMESPACE}
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: ${CONTROLLER}
EOF

read_token() {
  local secret="$1"
  local encoded=""
  local attempt
  for attempt in $(seq 1 30); do
    encoded="$(kubectl --kubeconfig "${DEFAULT_KUBECONFIG}" --context "${ADMIN_CONTEXT}" \
      get secret "${secret}" -n "${NAMESPACE}" -o jsonpath='{.data.token}')"
    if [[ -n "${encoded}" ]]; then
      printf '%s' "${encoded}" | base64 --decode
      return 0
    fi
    sleep 1
  done
  echo "Timed out waiting for token Secret ${secret}." >&2
  return 1
}

CALLER_TOKEN="$(read_token "${CALLER}-token")"
CONTROLLER_TOKEN="$(read_token "${CONTROLLER}-token")"
SERVER="$(kubectl --kubeconfig "${DEFAULT_KUBECONFIG}" --context "${ADMIN_CONTEXT}" \
  config view --raw --flatten --minify -o jsonpath='{.clusters[0].cluster.server}')"
CA_DATA="$(kubectl --kubeconfig "${DEFAULT_KUBECONFIG}" --context "${ADMIN_CONTEXT}" \
  config view --raw --flatten --minify -o jsonpath='{.clusters[0].cluster.certificate-authority-data}')"
if [[ -z "${SERVER}" || -z "${CA_DATA}" ]]; then
  echo "Could not read the kind cluster server and embedded CA data." >&2
  exit 1
fi

TEMP_DIR="$(mktemp -d)"
cleanup() {
  if [[ -n "${TEMP_DIR:-}" && -d "${TEMP_DIR}" ]]; then
    rm -f -- "${TEMP_DIR}/ca.crt"
    rmdir -- "${TEMP_DIR}"
  fi
}
trap cleanup EXIT
printf '%s' "${CA_DATA}" | base64 --decode >"${TEMP_DIR}/ca.crt"
TEMP_CONTROLLER="$(mktemp "$(dirname "${CONTROLLER_KUBECONFIG}")/.controller.kubeconfig.XXXXXX")"

kubectl --kubeconfig "${TEMP_CONTROLLER}" config set-cluster "${CLUSTER_NAME}" \
  --server="${SERVER}" --certificate-authority="${TEMP_DIR}/ca.crt" --embed-certs=true >/dev/null
kubectl --kubeconfig "${TEMP_CONTROLLER}" config set-credentials "${CONTROLLER}" \
  --token="${CONTROLLER_TOKEN}" >/dev/null
kubectl --kubeconfig "${TEMP_CONTROLLER}" config set-context "${CONTROLLER}" \
  --cluster="${CLUSTER_NAME}" --user="${CONTROLLER}" --namespace="${NAMESPACE}" >/dev/null
kubectl --kubeconfig "${TEMP_CONTROLLER}" config use-context "${CONTROLLER}" >/dev/null
chmod 600 "${TEMP_CONTROLLER}"
mv -f "${TEMP_CONTROLLER}" "${CONTROLLER_KUBECONFIG}"

# The privileged controller context must never be available through the
# ambient kubeconfig. The timestamped backup above keeps any prior entry
# recoverable while the active file is made safe for the caller.
if context_exists "${CONTROLLER}"; then
  kubectl --kubeconfig "${DEFAULT_KUBECONFIG}" config delete-context "${CONTROLLER}" >/dev/null
fi
kubectl --kubeconfig "${DEFAULT_KUBECONFIG}" config unset "users.${CONTROLLER}" >/dev/null 2>&1 || true
kubectl --kubeconfig "${DEFAULT_KUBECONFIG}" config set-credentials "${CALLER_CONTEXT}" \
  --token="${CALLER_TOKEN}" >/dev/null
kubectl --kubeconfig "${DEFAULT_KUBECONFIG}" config set-context "${CALLER_CONTEXT}" \
  --cluster="${CLUSTER_NAME}" --user="${CALLER_CONTEXT}" --namespace="${NAMESPACE}" >/dev/null
kubectl --kubeconfig "${DEFAULT_KUBECONFIG}" config use-context "${CALLER_CONTEXT}" >/dev/null
chmod 600 "${DEFAULT_KUBECONFIG}"

if context_exists "${CONTROLLER}"; then
  echo "Controller context unexpectedly remains in ${DEFAULT_KUBECONFIG}." >&2
  exit 1
fi

echo "Caller context ${CALLER_CONTEXT} is now active in ${DEFAULT_KUBECONFIG}."
echo "Admin context remains recoverable as ${ADMIN_CONTEXT}."
echo "Controller kubeconfig written to ${CONTROLLER_KUBECONFIG}."
