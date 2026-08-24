#!/usr/bin/env bash
# Shared app selection. Every stage sources this, so one variable switches the
# whole folder between applications:
#
#   SAFELANE_APP=safelane-demo-api ./cluster/install.sh
#
# Per-app data lives in cluster/apps/<app>/ -- app.env plus the two manifests.
# The scripts stay generic; only the data differs.
#
# SAFELANE_ENVIRONMENT names the environment these identities belong to. It is
# the second half of every derived SafeLane path, and it is read from the
# environment rather than from a configuration file on purpose: installing this
# cluster must not require SafeLane configuration to exist first.
CLUSTER_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
export PATH="${HOME}/.local/bin:${PATH}"

SAFELANE_APP="${SAFELANE_APP:-safelane-demo-api}"
SAFELANE_ENVIRONMENT="${SAFELANE_ENVIRONMENT:-production}"
APP_DIR="${CLUSTER_DIR}/apps/${SAFELANE_APP}"
if [ ! -f "${APP_DIR}/app.env" ]; then
  echo "unknown app '${SAFELANE_APP}'. Available: $(ls "${CLUSTER_DIR}/apps" | tr '\n' ' ')" >&2
  exit 2
fi
# shellcheck disable=SC1090
. "${APP_DIR}/app.env"
# Optional per-app data, defaulted so `set -u` cannot kill a stage that only
# mentions it in a summary line.
ANALYSIS_TEMPLATE="${ANALYSIS_TEMPLATE:-}"
export SAFELANE_APP SAFELANE_ENVIRONMENT APP_DIR NAMESPACE ROLLOUT ANALYSIS_TEMPLATE
export STABLE_SERVICE CANARY_SERVICE

# resolve_digest <repo> <tag> -- the immutable digest for a GHCR tag.
resolve_digest() {
  local repo="$1" tag="$2" token
  token="$(curl -fsSL "https://ghcr.io/token?scope=repository:${repo}:pull" \
    | sed -n 's/.*"token":"\([^"]*\)".*/\1/p')"
  if [ -z "${token}" ]; then
    echo "GHCR did not return a pull token for ${repo}." >&2
    return 1
  fi
  curl -fsSLI -H "Authorization: Bearer ${token}" \
    -H "Accept: application/vnd.oci.image.index.v1+json,application/vnd.docker.distribution.manifest.list.v2+json,application/vnd.oci.image.manifest.v1+json,application/vnd.docker.distribution.manifest.v2+json" \
    "https://ghcr.io/v2/${repo}/manifests/${tag}" \
    | grep -i '^docker-content-digest' | tr -d '\r' | awk '{print $2}'
}

# render <file> -- expand only the variables the manifests use, so a $ in the
# YAML itself survives.
render() {
  # `:-` throughout: apps that resolve their digest at run time have no
  # BASELINE_IMAGE yet when the monitoring stage renders, and `set -u` would
  # otherwise kill render and hand kubectl an empty document.
  NAMESPACE="${NAMESPACE:-}" ROLLOUT="${ROLLOUT:-}" BASELINE_IMAGE="${BASELINE_IMAGE:-}" \
  BASELINE_DIGEST="${BASELINE_DIGEST:-}" STABLE_SERVICE="${STABLE_SERVICE:-}" \
  CANARY_SERVICE="${CANARY_SERVICE:-}" \
    envsubst '$NAMESPACE $ROLLOUT $BASELINE_IMAGE $BASELINE_DIGEST $STABLE_SERVICE $CANARY_SERVICE' < "$1"
}
