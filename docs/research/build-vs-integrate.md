# SafeLane build-versus-integrate investigation

**Question:** Can an existing agent plus Argo Rollouts and narrow RBAC provide the same artifact-bound release experience without a separate SafeLane core?

**Research date:** 2026-08-15; product boundary updated 2026-08-24

## Verdict

The broad claim “an autonomous agent deploys software progressively” is already composable:

1. Stakpak supplies a general autonomous DevOps agent, operating procedures, and runtime guardrails.
2. kagent supplies a Kubernetes-native agent runtime and tools for Kubernetes, Argo, and observability.
3. Argo Rollouts supplies canary traffic progression, analysis, promotion, abort, and rollback.
4. `rollouts-plugin-metric-ai` plus `kubernetes-aiops-agent` supplies AI-driven canary analysis and remediation workflows.
5. Kubernetes RBAC supplies a real caller/controller capability boundary.

A demo that only shows an agent changing a Rollout and an AI metric deciding promotion is therefore an integration exercise, not a distinct product.

The narrower SafeLane hypothesis survives:

> SafeLane freezes the exact candidate artifact, deployed baseline, complete source delta, target, configured health analysis, and relevant history; validates a grounded proceed-or-wait recommendation; binds one approval to one narrow patch; stays attached while Argo executes; and stores compact proof.

No reviewed tool documents that complete pre-release-to-proof loop as one product contract. SafeLane is justified only if the join materially improves the experience and trust boundary.

## What existing tools already solve

### Stakpak

Stakpak documents an autonomous DevOps agent, Rulebooks for operational procedures, Autopilot for long-running work, and Warden for runtime guardrails ([Rulebooks](https://stakpak.gitbook.io/docs/how-it-works/rulebooks), [Autopilot](https://stakpak.gitbook.io/docs/how-it-works/autopilot), [Warden](https://stakpak.gitbook.io/docs/how-it-works/warden-guardrails)). It covers the broad agent experience. Its public material does not document SafeLane's exact deployed-to-candidate comparison, application-owned health-coverage assessment, single-use Argo patch approval, and per-Environment proof.

### kagent

kagent is a Kubernetes-native framework for agents, models, MCP tools, and controllers. Its built-in integrations cover Kubernetes, Argo, Prometheus, Grafana, Istio, Helm, and related operations ([kagent README](https://github.com/kagent-dev/kagent/blob/main/README.md); [kagent tools](https://github.com/kagent-dev/tools)). It solves agent hosting and tool access, not SafeLane's release evidence semantics or approval binding.

### AI rollout analysis

[`kubernetes-aiops-agent`](https://github.com/kdubois/kubernetes-aiops-agent) analyzes runtime logs, events, and metrics and can suggest remediation. [`rollouts-plugin-metric-ai`](https://github.com/argoproj-labs/rollouts-plugin-metric-ai) collects stable/canary context, asks an A2A agent for promote/rollback judgment, and returns a metric result to Argo.

These projects invalidate “AI watches a canary” as a differentiator. SafeLane's assessment happens before mutation and asks whether the complete candidate has credible hazards that the Application's configured analysis can observe.

### Argo Rollouts

[Argo Rollouts](https://github.com/argoproj/argo-rollouts) is the execution engine for canary steps, weighted exposure, background analysis, promotion, abort, and rollback. SafeLane should not rebuild those mechanics or generate application health checks.

### Kubernetes policy systems

[ValidatingAdmissionPolicy](https://kubernetes.io/docs/reference/access-authn-authz/validating-admission-policy/), [Kyverno](https://github.com/kyverno/kyverno), and [Gatekeeper](https://open-policy-agent.github.io/gatekeeper/website/docs/) can enforce general resource rules. They remain useful platform controls, but Plan 41 does not require SafeLane to become an admission service or policy engine. SafeLane instead constrains its own credential and patch construction.

## What SafeLane reuses

- **Agent experience:** the active Claude or Codex session plus one installed SafeLane skill.
- **Source and CI facts:** GitHub repository, default-head, compare, checks, and workflow data.
- **Artifact identity:** OCI Distribution, standard Docker credentials, OCI labels, and optional CI provenance.
- **Rollout execution:** Argo Rollouts and the Application's existing background AnalysisTemplates.
- **Access control:** separate caller and controller Kubernetes identities.
- **Evaluation:** fixed assessment scenarios and evaluation-only traces, outside production history.

## What SafeLane must uniquely join

1. **Complete release identity:** one immutable candidate, deployed baseline, and every included source change.
2. **Deployment-specific judgment:** hazards grounded in frozen evidence and mapped honestly to configured health coverage.
3. **Legible recommendation:** proceed or wait, with actual rollout percentages rather than an unexplained score.
4. **Exact authority:** one approval bound to the candidate, target, recommendation, analysis, and a patch that replaces only image and canary steps.
5. **Attached outcome:** reconciliation against Argo plus compact history and detailed Release Proof.

The novelty is not any individual component. It is whether this exact join makes agent-driven releases meaningfully easier to understand and safer to authorize.

## Scope consequences

Keep out of the POC:

- a general DevOps agent or model-provider runtime;
- a custom LLM canary-analysis engine;
- a rollout controller, traffic router, or probe system;
- generated Application manifests or health checks;
- a proprietary provenance/signing system;
- admission policy, a generic policy language, or a numeric risk formula;
- dashboards, non-Argo integrations, multi-cluster federation, and production hardening.

## Falsifying comparison

Combine a capable agent, GitHub/OCI evidence, narrow Kubernetes identities, and Argo Rollouts. If that composition already:

- identifies the exact candidate and deployed baseline;
- assesses the complete accumulated change against the target's real health analysis;
- presents a grounded recommendation and actual rollout sequence;
- binds one approval to one narrow, stale-safe patch;
- stays attached without gate-by-gate approval; and
- retains compact history plus detailed proof,

then a separate SafeLane core is not justified. If the composition repeatedly reconstructs or weakens that seam, SafeLane survives as the small domain-specific layer that makes it durable.

## Decision

Proceed with the composition-first POC in Plan 41. Be explicit that GitHub, OCI, Claude, Kubernetes, and Argo provide the underlying capabilities. SafeLane owns the frozen release boundary, validated recommendation, exact approval, narrow patch, attached coordination, and proof—and nothing broader until the demo demonstrates a repeated need.
