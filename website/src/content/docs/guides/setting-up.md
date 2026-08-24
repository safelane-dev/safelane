---
title: Register an Application
description: Register one existing Argo canary Rollout.
---

## Requirements

You need:

- an application repository with a GitHub origin;
- a kubeconfig context that can read one namespace;
- an Argo canary Rollout with inline containers;
- stable and canary Services when the Rollout uses traffic routing;
- a background AnalysisTemplate;
- a traceable OCI image.

See [Compatibility](../reference/compatibility/) for all requirements.

## Register

From the application repository, ask:

> Register this application in production.

Select the Environment, Rollout, and container. SafeLane validates the Services and health analysis.

SafeLane shows `safelane.yml` before it writes the file to:

```text
~/.safelane/apps/<application>/safelane.yml
```

Registration does not create or change Kubernetes resources.

Run registration again after a target change. SafeLane shows the difference and preserves the `policy` block.

Next: [Run a release](./release-end-to-end/).
