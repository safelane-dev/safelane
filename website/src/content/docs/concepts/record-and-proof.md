---
title: History & Proof
description: Learn what SafeLane records for each release.
---

SafeLane stores compact history and detailed proof.

## History

The assessment reads up to ten recent releases for the same Application and Environment. Each item has:

- time and candidate;
- recommendation and lane;
- one important reason;
- outcome.

History gives context. It cannot remove a known hazard.

## Release Proof

Each release attempt stores:

- the frozen Release Delta;
- the final recommendation;
- the approved patch;
- execution events;
- the final outcome.

SafeLane does not store the chat, secret values, or unused assessment drafts.

Show the short proof:

```bash
safelane proof production
```

Add `--details` to show exact evidence and events.

Next: [Release data](../reference/release-record/).
