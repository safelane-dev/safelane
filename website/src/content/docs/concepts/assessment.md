---
title: Assessment & Recommendation
description: Learn how SafeLane recommends proceed or wait.
---

SafeLane assesses one release. It does not review general code quality.

## Eligibility

SafeLane stops before assessment when:

- CI did not pass;
- the OCI image does not match the candidate;
- the running version is unknown;
- the source histories are not related.

## Evidence

The agent reads four views:

| View | Content |
| --- | --- |
| Changes | All changes from the running version to the candidate |
| Deployment | Environment, Rollout, container, and patch |
| Health | Configured analysis and measurement rules |
| History | Up to ten recent releases for this target |

The agent can open more evidence for a specific question. It asks you one question only when a required fact is not available.

## Recommendation

**Proceed** means that a configured release lane can control the identified hazards.

**Wait** means that evidence is missing or a health check does not cover a credible hazard.

SafeLane validates each recommendation. If the result is not valid after one correction, SafeLane recommends **wait**.

Next: [Release lanes](./release-policy/) and [approval boundary](./boundary/).
