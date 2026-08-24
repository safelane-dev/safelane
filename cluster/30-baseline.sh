#!/usr/bin/env bash
# Seeds (or resets) the application namespace to a Healthy Rollout at an older
# image, so the next release has a real stable version to canary against.
# Without this, Argo Rollouts treats the Rollout's first-ever apply as having no
# prior version and skips every canary step, going straight to weight 100.
#
# Safe to run twice: it deletes and recreates only the Rollout -- the one
# resource whose *history* matters -- so every re-run replays that same
# "first apply, no history" path deterministically. The Services and any
# AnalysisTemplate or Ingress are idempotent.
set -euo pipefail
. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib.sh"

# Apps pin either an exact digest or a tag resolved at run time. Either way the
# Rollout is bound to a digest, never a mutable tag.
if [ -z "${BASELINE_DIGEST:-}" ]; then
  BASELINE_DIGEST="$(resolve_digest "${BASELINE_IMAGE_REPO#ghcr.io/}" "${BASELINE_TAG}")"
fi
BASELINE_IMAGE="${BASELINE_IMAGE_REPO}@${BASELINE_DIGEST}"
echo "app ${SAFELANE_APP} -> ${BASELINE_IMAGE}"

kubectl create namespace "${NAMESPACE}" --dry-run=client -o yaml | kubectl apply -f -
render "${APP_DIR}/supporting.yaml" | kubectl apply -n "${NAMESPACE}" -f -

kubectl delete rollout "${ROLLOUT}" -n "${NAMESPACE}" --ignore-not-found --wait=true
render "${APP_DIR}/rollout.yaml" | kubectl apply -n "${NAMESPACE}" -f -

echo "Waiting for Healthy…"
kubectl argo rollouts status "${ROLLOUT}" -n "${NAMESPACE}" --timeout=180s
echo "Baseline seeded. SafeLane will canary against this version."
