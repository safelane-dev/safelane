---
title: Exit Codes
description: Process outcomes for direct CLI use.
---

| Code | Meaning | Action |
| ---: | --- | --- |
| `0` | The command completed. | Use the returned state. |
| `1` | The operation failed. | Report the reason and stop. |
| `2` | The command or input is not valid. | Correct the input. |
| `3` | The mutation outcome is unknown. | Run `safelane status <env>`. |

Do not repeat a mutation after code `3`. Argo can accept a change before a connection fails.

A **wait** recommendation is a command result. It is not an invented failure or risk level.

Next: [CLI](./cli/).
