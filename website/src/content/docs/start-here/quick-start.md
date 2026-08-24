---
title: Quick Start
description: See one SafeLane release from registration to proof.
---

This example uses `payments-api`. The application already has an Argo canary Rollout and background health analysis.

## 1. Register the application

Ask your coding agent:

> Register this application in production.

Select the Rollout and container. SafeLane writes one `safelane.yml` file.

<figure class="terminal-figure">
  <img src="/safelane/examples/registration-example.png" alt="SafeLane registers payments-api in production and shows its release boundary." />
</figure>

## 2. Request a release

> Deploy payments-api to production.

SafeLane checks the candidate, CI result, OCI image, running version, and target. It then assesses all changes since the running version.

## 3. Approve the recommendation

SafeLane recommends **proceed** or **wait**. A proceed recommendation shows the rollout steps and health analysis.

<figure class="terminal-figure">
  <img src="/safelane/examples/recommendation-example.png" alt="SafeLane recommends a 50 to 100 percent rollout and asks for approval." />
</figure>

Approve the final question. The approval is valid for this release only.

## 4. Follow the release

SafeLane changes the selected image and canary steps. It waits for new health results at each pause. Argo controls failure, abort, and rollback.

<figure class="terminal-figure">
  <img src="/safelane/examples/proof-example.png" alt="SafeLane completes the release and shows proof for payments-api." />
</figure>

You can now [register your application](../../guides/setting-up/) or [run the local demo](../../guides/local-demo/).
