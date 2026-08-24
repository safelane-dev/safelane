---
title: Use the Agent Skill
description: Use SafeLane from Claude or Codex.
---

The installer adds the `/safelane` skill to Claude and Codex. Restart the agent after installation.

The skill:

- translates natural requests into SafeLane commands;
- reads required release evidence;
- asks one question at a time when necessary;
- shows the final approval boundary;
- follows the rollout to an outcome.

The skill cannot bypass eligibility, reuse approval, change the patch boundary, or replace Argo health checks.

## Example requests

```text
Register this application in production.
Deploy payments-api to production.
Why do you recommend this lane?
What is the release waiting for?
Hold production while we investigate.
Continue production; the incident is resolved.
Stop production; customers report errors.
Show the detailed proof.
```

Use the [CLI reference](../reference/cli/) for direct commands.
