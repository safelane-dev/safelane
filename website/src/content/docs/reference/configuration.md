---
title: Configuration
description: The safelane.yml file that registration creates.
---

SafeLane stores one file per Application:

```text
~/.safelane/apps/<application>/safelane.yml
```

```yaml
application:
  name: payments-api
  repository: acme/payments-api

artifact:
  container: payments-api
  image: ghcr.io/acme/payments-api

environments:
  - name: production
    impact: critical
    kubernetes:
      context: safelane-caller-payments-api
      namespace: payments
      rollout: payments-api

policy:
  default_lane: guarded
  risk_mapping:
    low: fast
    medium: standard
    high: guarded
  lanes:
    fast: { weights: [50, 100] }
    standard: { weights: [25, 50, 100] }
    guarded: { weights: [25, 50, 75, 100] }
```

Registration discovers the repository, image, and Kubernetes fields. You can edit the `policy` block.

Lane percentages must increase and end at 100. Each mapping must name an existing lane.

Next: [Release lanes](../concepts/release-policy/).
