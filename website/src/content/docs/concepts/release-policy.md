---
title: Release Lanes
description: Learn how SafeLane selects Argo canary steps.
---

A release lane is a configured sequence of canary percentages.

| Lane | Steps |
| --- | --- |
| `fast` | 50% → health → 100% |
| `standard` | 25% → health → 50% → health → 100% |
| `guarded` | 25% → health → 50% → health → 75% → health → 100% |

SafeLane maps its internal assessment result to one lane. The agent cannot create new percentages during a release.

At each pause, SafeLane waits for a new successful result from the configured background AnalysisRun. Missing measurements do not count as success.

You can edit the `policy` block in `safelane.yml`. Each lane must:

- use increasing percentages;
- end at 100;
- have a valid risk mapping.

Replica-based percentages are approximate unless the application has a traffic router.

Next: [Configuration](../reference/configuration/) and [assessment](./assessment/).
