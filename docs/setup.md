# Register an application

SafeLane registers an existing Argo canary. It does not create Kubernetes resources or health analysis.

## Requirements

- A GitHub origin.
- Read access to one Kubernetes namespace.
- An Argo canary Rollout with inline containers.
- Stable and canary Services when the Rollout uses traffic routing.
- A background AnalysisTemplate.
- A traceable OCI image.

## Register

From the application repository, ask Claude or Codex:

```text
Register this application in production.
```

Select the Environment, Rollout, and container. SafeLane shows the configuration before it writes:

```text
~/.safelane/apps/<application>/safelane.yml
```

Run registration again after a target change. SafeLane shows the difference and preserves the `policy` block.

## Direct commands

```bash
safelane discover <namespace>
safelane register <selection-json-path|->
```

For a local environment, run `./cluster/install.sh`.

Read the [registration guide](https://andrewmaged814.github.io/safelane/guides/setting-up/) and [compatibility reference](https://andrewmaged814.github.io/safelane/reference/compatibility/).
