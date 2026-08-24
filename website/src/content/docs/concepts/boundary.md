---
title: Approval Boundary
description: Learn what one SafeLane approval permits.
---

SafeLane starts the release only after one clear approval.

## One release

The approval applies to:

- one Application and Environment;
- one source revision and OCI digest;
- one running baseline;
- one health analysis set;
- one release lane;
- one patch.

SafeLane checks these facts again before it changes the Rollout. A change cancels the approval.

## Two changes

SafeLane can replace only:

```text
/spec/template/spec/containers/<selected-index>/image
/spec/strategy/canary/steps
```

It does not change probes, resources, secrets, Services, or health analysis.

## Two identities

The caller can read the target. The SafeLane controller can read and patch only the registered Rollout. It can also read its AnalysisRuns.

Argo remains the authority for health, abort, and rollback.

Next: [Run a release](../guides/release-end-to-end/) and [monitor a release](../guides/rollout-recovery/).
