---
title: CLI
description: SafeLane commands for direct use and automation.
---

SafeLane infers the Application from the current Git repository. The Environment is a positional argument.

## Registration

| Command | Result |
| --- | --- |
| `safelane discover <namespace>` | List compatible Rollouts and containers. |
| `safelane register <selection-json-path\|->` | Validate and preview the exact configuration; at a terminal, confirm before writing. |

## Release

| Command | Result |
| --- | --- |
| `safelane inspect <env> [<revision>]` | Check eligibility and freeze the Release Delta. |
| `safelane recommend <env> <assessment-json-path\|->` | Validate and freeze a recommendation. |
| `safelane run <env>` | Apply the approved patch and monitor Argo. |
| `safelane status <env>` | Show the active release. |
| `safelane hold <env> <reason>` | Stop progression. |
| `safelane continue <env> <reason>` | Continue a held release. |
| `safelane stop <env> <reason>` | Ask Argo to abort and restore stable. |
| `safelane proof <env> [--details]` | Show release proof. |

Use `--app <application>` when repository matching is not unique. Piped output
is structured JSON; use `--json` only to force JSON at a terminal.

Next: [Exit codes](./exit-codes/).
