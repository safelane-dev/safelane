---
title: What is SafeLane?
description: Learn what SafeLane does and where its authority stops.
---

SafeLane releases software through an existing Argo canary Rollout.

It gives a coding agent the facts and controls for one release. The agent does not build a new release process in each chat.

## What SafeLane does

SafeLane:

- identifies the exact source revision and OCI image;
- compares the candidate with the version that runs now;
- checks CI, the target, and release history;
- recommends **proceed** or **wait**;
- shows the canary steps and health checks;
- asks for one release approval;
- monitors Argo until the release stops or completes;
- stores release proof.

## The approval boundary

Your approval applies to one candidate, target, and patch. SafeLane cancels the approval if one of these facts changes.

SafeLane changes only:

```text
the selected container image
the Argo canary steps
```

Argo evaluates health. Argo also controls abort and rollback.

## What SafeLane does not do

SafeLane does not provision Kubernetes. It does not replace code review. It does not claim that code is safe.

Next: [See a release](./quick-start/) or [check compatibility](../reference/compatibility/).
