---
title: Compatibility
description: Check if SafeLane can register your application.
---

## Required

- A GitHub repository.
- Read access to one Kubernetes namespace.
- An Argo canary Rollout with inline containers.
- Stable and canary Services.
- At least one background AnalysisTemplate.
- An OCI-compatible image registry.
- Proof that links the candidate image to its source revision.
- A controller that can patch the Rollout and read AnalysisRuns.

SafeLane resolves each selected image to an immutable digest.

## Artifact proof

SafeLane accepts OCI labels, CI provenance, prior SafeLane history, or a verified one-time baseline. It does not infer a revision from a tag or timestamp.

## Not supported

- blue-green Rollouts;
- `workloadRef`;
- inline-only analysis;
- advanced step plugins;
- Argo CD or Flux ownership of the Rollout;
- non-Argo rollout engines;
- cluster or application provisioning.

Next: [Register an application](../guides/setting-up/).
