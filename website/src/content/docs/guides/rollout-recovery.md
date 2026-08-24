---
title: Monitor & Control
description: Show status or control an active release.
---

SafeLane reads the Rollout before it reports status. The observed Rollout is the source of truth.

## Show status

```bash
safelane status production
```

The result shows the current exposure and the next health gate.

## Hold

```bash
safelane hold production "waiting for on-call"
```

Hold prevents more progression. It does not change exposure.

## Continue

```bash
safelane continue production "on-call is ready"
```

SafeLane continues from the observed Argo state.

## Stop

```bash
safelane stop production "customers report errors"
```

SafeLane asks Argo to abort and restore the stable version. A retry is a new release with new approval.

Next: [History and proof](../concepts/record-and-proof/).
