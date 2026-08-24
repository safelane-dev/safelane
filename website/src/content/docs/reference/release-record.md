---
title: Release Data
description: Files that SafeLane stores for release history and proof.
---

```text
~/.safelane/apps/<application>/environments/<environment>/history.jsonl
~/.safelane/apps/<application>/environments/<environment>/releases/<attempt>.json
```

## History

Each history item has the time, candidate, recommendation, lane, one reason, and outcome. Assessment reads up to ten recent items.

## Release attempt

Each attempt stores:

- Release Delta;
- Release Recommendation;
- Release Patch;
- execution events;
- Approval and execution events.

SafeLane does not store chats, credentials, secret values, or abandoned drafts.

```bash
safelane proof <env>
safelane proof <env> --details
```

The first command shows a compact result. The second command shows frozen evidence and events.

Next: [History and proof](../concepts/record-and-proof/).
