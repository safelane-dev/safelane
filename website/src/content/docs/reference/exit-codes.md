---
title: Exit Codes
description: Process outcomes for direct CLI use.
---

| Code | Meaning | Action |
| ---: | --- | --- |
| `0` | The command completed. | Use the returned state. |
| `1` | The operation failed. | Report the reason and stop. |
| `2` | The command or input is not valid. | Correct the input. |
| `4` | The submitted assessment needs its one correction. | Correct only the cited problems and resubmit once. |

If an attached run is interrupted, use status and run the same Environment
again. SafeLane reconnects to the active attempt without applying its patch a
second time.

A **wait** recommendation is a command result. It is not an invented failure or risk level.

Next: [CLI](./cli/).
