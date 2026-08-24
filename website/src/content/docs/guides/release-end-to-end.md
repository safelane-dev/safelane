---
title: Run a Release
description: Assess, approve, and monitor one release.
---

## 1. Request

From the registered application repository, ask:

> Deploy payments-api to production.

Name a source revision only when you do not want the current default-branch head.

## 2. Check

SafeLane checks CI, the OCI image, the running version, the target Rollout, and health analysis.

The release stops if a required fact is not valid. SafeLane does not use an older green candidate.

## 3. Review

A **proceed** recommendation shows the evidence, hazards, health checks, and rollout percentages.

A **wait** recommendation shows the blocking concern and one next action.

## 4. Approve

Approve the final release question. SafeLane checks all material facts again. It then changes only the selected image and canary steps.

## 5. Monitor

SafeLane waits for a new successful health result at each pause. Argo controls abort and rollback.

You can ask SafeLane to show status, hold, continue, stop, or show proof.

Next: [Monitor and control](./rollout-recovery/).
