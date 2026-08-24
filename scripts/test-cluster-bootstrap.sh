#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TEST_ROOT="$(mktemp -d)"
REAL_BASH="${BASH}"

cleanup() {
  rm -rf -- "${TEST_ROOT}"
}
trap cleanup EXIT HUP INT TERM

write_install_fakes() {
  local bin="$1"
  mkdir -p "${bin}"

  cat >"${bin}/kubectl" <<'EOF'
#!/bin/sh
set -eu
case "$*" in
  "argo rollouts version --short")
    [ "${PLUGIN_AVAILABLE:-0}" = 1 ] || exit 1
    echo v1.9.1
    ;;
  "config get-contexts -o name")
    [ -f "${CLUSTER_READY}" ] && echo minikube
    ;;
  "config use-context minikube") ;;
  "auth can-i * *")
    [ -f "${CLUSTER_READY}" ] && echo yes || echo no
    ;;
  "config current-context")
    [ -f "${CLUSTER_READY}" ] && echo minikube
    ;;
esac
EOF

  cat >"${bin}/bash" <<'EOF'
#!/bin/sh
set -eu
case "${1##*/}" in
  10-cluster.sh) : >"${CLUSTER_READY}" ;;
esac
EOF
  chmod 755 "${bin}/kubectl" "${bin}/bash"
}

assert_fresh_cluster_starts_before_admin_check() {
  local case_dir="${TEST_ROOT}/fresh" bin output marker
  bin="${case_dir}/bin"
  marker="${case_dir}/cluster-ready"
  output="${case_dir}/output"
  mkdir -p "${case_dir}"
  write_install_fakes "${bin}"

  if ! PATH="${bin}:${PATH}" CLUSTER_READY="${marker}" PLUGIN_AVAILABLE=1 \
    "${REAL_BASH}" "${ROOT_DIR}/cluster/install.sh" >"${output}" 2>&1; then
    cat "${output}" >&2
    echo "fresh bootstrap checked admin access before starting minikube" >&2
    exit 1
  fi
  [ -f "${marker}" ] || {
    echo "fresh bootstrap never started the cluster stage" >&2
    exit 1
  }
}

assert_missing_plugin_fails_before_mutation() {
  local case_dir="${TEST_ROOT}/plugin" bin output marker
  bin="${case_dir}/bin"
  marker="${case_dir}/cluster-ready"
  output="${case_dir}/output"
  mkdir -p "${case_dir}"
  write_install_fakes "${bin}"

  if PATH="${bin}:${PATH}" CLUSTER_READY="${marker}" PLUGIN_AVAILABLE=0 \
    "${REAL_BASH}" "${ROOT_DIR}/cluster/install.sh" >"${output}" 2>&1; then
    echo "bootstrap accepted a missing Argo Rollouts kubectl plugin" >&2
    exit 1
  fi
  [ ! -e "${marker}" ] || {
    echo "bootstrap mutated the cluster before reporting the missing plugin" >&2
    exit 1
  }
  grep -q "Argo Rollouts kubectl plugin" "${output}" || {
    cat "${output}" >&2
    echo "missing plugin failure did not explain the prerequisite" >&2
    exit 1
  }
}

assert_cluster_stage_preserves_renamed_admin_context() {
  local case_dir="${TEST_ROOT}/admin-context" bin output log
  bin="${case_dir}/bin"
  output="${case_dir}/output"
  log="${case_dir}/kubectl.log"
  mkdir -p "${bin}"

  cat >"${bin}/minikube" <<'EOF'
#!/bin/sh
if [ "${1:-}" = status ]; then
  echo Running
fi
EOF
  cat >"${bin}/kubectl" <<'EOF'
#!/bin/sh
set -eu
echo "$*" >>"${KUBECTL_LOG}"
case "$*" in
  "config get-contexts -o name") echo safelane-admin ;;
  "auth can-i * *") echo yes ;;
esac
EOF
  chmod 755 "${bin}/minikube" "${bin}/kubectl"

  if ! PATH="${bin}:${PATH}" KUBECTL_LOG="${log}" \
    "${REAL_BASH}" "${ROOT_DIR}/cluster/10-cluster.sh" >"${output}" 2>&1; then
    cat "${output}" >&2
    echo "cluster stage could not reuse the renamed administrator context" >&2
    exit 1
  fi
  grep -q '^config use-context safelane-admin$' "${log}" || {
    cat "${log}" >&2
    echo "cluster stage did not preserve safelane-admin" >&2
    exit 1
  }
  if grep -q '^config use-context minikube$' "${log}"; then
    cat "${log}" >&2
    echo "cluster stage tried to use the removed minikube context" >&2
    exit 1
  fi
}

assert_ingress_addon_creation_is_waited_for() {
  local case_dir="${TEST_ROOT}/ingress-race" bin output state
  bin="${case_dir}/bin"
  output="${case_dir}/output"
  state="${case_dir}/state"
  mkdir -p "${bin}"

  cat >"${bin}/minikube" <<'EOF'
#!/bin/sh
if [ "${1:-}" = status ]; then
  echo Running
elif [ "${1:-}" = addons ] && [ "${2:-}" = enable ]; then
  :
fi
EOF
  cat >"${bin}/kubectl" <<'EOF'
#!/bin/sh
set -eu
case "$*" in
  "config get-contexts -o name") echo minikube ;;
  "config use-context minikube") ;;
  "auth can-i * *") echo yes ;;
  "get namespace ingress-nginx")
    if [ -f "${STATE}.namespace" ]; then exit 0; fi
    : >"${STATE}.namespace"
    exit 1
    ;;
  "get deployment ingress-nginx-controller -n ingress-nginx")
    [ -f "${STATE}.namespace" ] || exit 1
    if [ -f "${STATE}.deployment" ]; then exit 0; fi
    : >"${STATE}.deployment"
    exit 1
    ;;
  "rollout status -n ingress-nginx deploy/ingress-nginx-controller --timeout=300s")
    [ -f "${STATE}.deployment" ] || {
      echo "rollout checked before ingress Deployment existed" >&2
      exit 1
    }
    ;;
esac
EOF
  chmod 755 "${bin}/minikube" "${bin}/kubectl"

  if ! PATH="${bin}:${PATH}" STATE="${state}" SAFELANE_WAIT_INTERVAL=0 \
    "${REAL_BASH}" "${ROOT_DIR}/cluster/10-cluster.sh" >"${output}" 2>&1; then
    cat "${output}" >&2
    echo "cluster stage did not wait for the ingress addon resources" >&2
    exit 1
  fi
}

assert_monitoring_uses_real_values_file() {
  local case_dir="${TEST_ROOT}/monitoring" bin output log
  bin="${case_dir}/bin"
  output="${case_dir}/output"
  log="${case_dir}/helm.log"
  mkdir -p "${bin}"

  cat >"${bin}/envsubst" <<'EOF'
#!/bin/sh
cat
EOF
  cat >"${bin}/kubectl" <<'EOF'
#!/bin/sh
exit 0
EOF
  cat >"${bin}/helm" <<'EOF'
#!/bin/sh
set -eu
if [ "${1:-}" = upgrade ]; then
  previous=""
  for argument in "$@"; do
    if [ "${previous}" = -f ]; then
      case "${argument}" in
        /dev/fd/*|/proc/*)
          echo "native Windows Helm cannot read ${argument}" >&2
          exit 1
          ;;
      esac
      [ -f "${argument}" ] || {
        echo "Helm values are not a real file: ${argument}" >&2
        exit 1
      }
    fi
    previous="${argument}"
  done
  echo "$*" >>"${HELM_LOG}"
fi
EOF
  chmod 755 "${bin}/envsubst" "${bin}/kubectl" "${bin}/helm"

  if ! PATH="${bin}:${PATH}" HELM_LOG="${log}" \
    "${REAL_BASH}" "${ROOT_DIR}/cluster/20-monitoring.sh" >"${output}" 2>&1; then
    cat "${output}" >&2
    echo "monitoring passed a virtual Bash file to a native command" >&2
    exit 1
  fi
  [ "$(wc -l <"${log}")" -eq 2 ] || {
    echo "monitoring test did not observe both Helm releases" >&2
    exit 1
  }
}

assert_fresh_cluster_starts_before_admin_check
assert_missing_plugin_fails_before_mutation
assert_cluster_stage_preserves_renamed_admin_context
assert_ingress_addon_creation_is_waited_for
assert_monitoring_uses_real_values_file

echo "Cluster bootstrap tests passed"
