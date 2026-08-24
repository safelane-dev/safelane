# SafeLane

SafeLane coordinates one approved, evidence-bound application release through
an existing Argo Rollout to a terminal outcome.

## Language

**Release Request**:
The user's request to release an Application to an Environment. It may include
an exact source revision.
_Avoid_: Pull request release, release the latest PR

**Application**:
One independently releasable workload, such as `payments-api`.
_Avoid_: Repository, pull request

**Environment**:
A named place where an Application runs, such as `production` or `staging`.
It has a confirmed impact and one registered Deployment Target.
_Avoid_: Namespace, cluster

**Artifact**:
The immutable OCI image that is running or proposed for release, bound to an
exact source revision by verified metadata.
_Avoid_: Mutable tag, build name

**Release Candidate**:
The exact Artifact proposed for an Environment, its source revision, CI result,
and all changes since the running baseline. Pull requests and commits are
provenance; SafeLane releases the Artifact.
_Avoid_: Pull request, latest build

**Deployment Target**:
The current Kubernetes context, namespace, Argo Rollout, and selected container
that implement one Application Environment.
_Avoid_: Environment

**Registration**:
The confirmed mapping from an Application and Environment to a discovered
Deployment Target. Registration previews and writes one minimal `safelane.yml`.
It does not provision Kubernetes resources.
_Avoid_: Generated setup plan, generated baseline

**Release Settings**:
The configured default lane, risk-to-lane mapping, and lane weights in
`safelane.yml`. Registration writes product defaults once and preserves later
user edits.
_Avoid_: Policy engine, generated policy

**Release Delta**:
The frozen evidence for one candidate: changes, deployment, configured health
analysis, relevant history, and the proposed patch. It is content-addressed and
excludes secret values.
_Avoid_: Change Dossier, prompt context

**Eligibility**:
The evidence-based answer to whether the exact candidate may be assessed for
release. Failed CI, missing Artifact binding, or an invalid change relationship
is ineligible, not high risk.

**Deployment Assessment**:
The active agent's grounded judgement of credible deployment hazards for a
Release Delta. SafeLane validates its snapshot, evidence citations, causal
structure, coverage, and configured lane; it does not grade the semantics with
path or size rules.
_Avoid_: General code review, deterministic risk score

**Hazard**:
A credible way this release could cause harm, with cited evidence,
preconditions, and a concrete consequence.
_Avoid_: Vague concern, lint finding

**Coverage**:
Whether the Application's configured background health analysis would detect a
Hazard: covered, partially covered, not covered, or unknown.
_Avoid_: SafeLane-generated assertion

**Recommendation**:
The grounded result of a Deployment Assessment: Proceed through one configured
Release Lane, or Wait with one useful next step.
_Avoid_: Authorization, prediction that code is safe

**Release Lane**:
A configured sequence of bounded canary exposures. The defaults are Fast
(`50 → 100`), Standard (`25 → 50 → 100`), and Guarded
(`25 → 50 → 75 → 100`).
_Avoid_: Agent-generated weights, user-named lane during approval

**Release Patch**:
The exact JSON Patch that changes only the selected container image and Argo
canary steps, guarded by the observed Rollout identity and version.
_Avoid_: Rendered bundle, generated Kubernetes manifest

**Approval**:
One explicit human decision bound to the exact candidate, target,
recommendation, and Release Patch. It is spent once. A recommendation is not
approval.
_Avoid_: Reusable approval, `--yes`

**Release Attempt**:
One Release Delta, Recommendation, Approval, Release Patch, event history, and
terminal outcome. A retry is a new attempt.

**Release Run**:
The attached, reconnectable coordination loop. It asks Argo to progress only
after a fresh successful background measurement at the current gate and stays
attached until completion or stop.
_Avoid_: Gate-by-gate approval, autonomous deployment engine

**Argo Abort**:
Argo Rollouts' response to failed configured analysis. Argo owns analysis,
traffic restoration, and normal rollback; SafeLane observes and records it.
_Avoid_: SafeLane rollback

**Release Control**:
The user's hold, continue, or stop action, recorded with a reason. The observed
Rollout is authoritative when it disagrees with SafeLane's record.
_Avoid_: Emergency ID, direct gate mutation

**Release Proof**:
The compact history card or detailed durable record of a Release Attempt.
Detailed proof loads only when requested.
_Avoid_: Conversation transcript, tool trace

## Evidence language

Describe evidence by the action that established it: configured, discovered,
confirmed, provided, or approved. Do not use ownership labels when the concrete
action is clearer.
