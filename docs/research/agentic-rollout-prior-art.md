# Agentic rollout prior art

**Question:** Is “a smart agentic way of deploying rollouts” an open product category, or is it already covered by existing projects?

**Research date:** 2026-08-24

## Verdict

The broad category is already occupied. Argo Rollouts provides the progressive-delivery engine; `rollouts-plugin-metric-ai` and `kubernetes-aiops-agent` demonstrate an agent judging a live canary and returning a promote-or-fail result; kagent supplies a general Kubernetes agent runtime with operational tools and human approval; and Stakpak positions a general DevOps agent around deployment automation, reusable operational knowledge, and runtime guardrails.

SafeLane therefore cannot differentiate on “AI deploys a canary” or “an agent watches a rollout.” The product boundary in [Plan 41](../plans/41.md) is narrower and still distinct in this source set:

> SafeLane resolves the exact candidate Artifact and deployed baseline into a frozen Release Delta, grounds one deployment recommendation in that evidence, binds one explicit approval to the exact target and Release Patch, stays attached while the existing Argo Rollout executes, and stores compact proof of the outcome.

This is a coordination and approval boundary, not an independent permit service or Kubernetes admission controller. Argo remains responsible for traffic progression, configured health analysis, abort, and rollback. SafeLane adds the release context and human decision that precede that execution, limits its own mutation to the selected image and canary steps, and preserves the resulting proof.

No claim is made that no other project implements this combination. The conclusion is limited to the five projects and pinned primary sources reviewed below.

## Comparison

| Project | What it does | Approval and execution boundary | Overlap with SafeLane | Gap relative to Plan 41 |
| --- | --- | --- | --- | --- |
| Argo Rollouts | Executes canary and blue-green strategies, traffic weighting, analysis, promotion, abort, and rollback | Kubernetes write access determines who may change the Rollout; the Argo controller executes the submitted spec | The complete progressive-delivery execution layer | Does not assemble an artifact-bound Release Delta, produce a source-aware pre-release recommendation, or bind a user approval to SafeLane's exact patch and stored proof |
| `rollouts-plugin-metric-ai` | Turns an A2A agent's structured `promote` result into an Argo analysis measurement | The remote agent supplies the judgment; Argo acts on the measurement | AI-assisted rollout judgment and automatic progression/abort | Evaluates the live canary from rollout/pod context rather than freezing the full baseline-to-candidate release context and obtaining one exact pre-mutation approval |
| `kubernetes-aiops-agent` | Collects Kubernetes diagnostics and metrics, uses an LLM workflow to decide promotion, and can open remediation issues or pull requests | Its read-oriented Kubernetes service account gathers evidence; the result influences deployment through the Argo metric plugin | Diagnosis, runtime evidence collection, rollout judgment, and remediation | Does not coordinate the artifact-resolution, pre-release recommendation, exact approval, narrow patch, attached execution, and proof lifecycle |
| kagent | Runs Kubernetes-native agents with MCP/A2A integrations, operational tools, sessions, and optional human approval | Tool credentials and RBAC grant authority; approval can gate an individual pending tool call | Agent runtime, Kubernetes/Argo tooling, persistence, and human-in-the-loop mechanics | A framework rather than a release-specific evidence, recommendation, approval, execution, and proof contract |
| Stakpak Agent | Automates infrastructure and deployment work using rulebooks, profiles, tool controls, secret substitution, and Warden | Agent profiles and runtime/network controls constrain what the agent may do | Broad autonomous DevOps workflow, reusable operational knowledge, and guardrails | Does not expose the reviewed SafeLane-shaped contract around a frozen Release Delta, one exact approval, a constrained Argo Release Patch, and attached release proof |

## Project findings

### 1. Argo Rollouts

Argo Rollouts already owns the deployment mechanics SafeLane should reuse. Its controller and CRDs provide canary and blue-green deployment, weighted traffic shifting, experiments, automated promotion and rollback, and analysis against external metrics ([project README](https://github.com/argoproj/argo-rollouts/blob/f2c5c2b51ff5ef0b071fcf9883614907aa055c52/README.md)). Canary steps encode weights and pauses. Background or inline `AnalysisRun`s can cause a rollout to continue, abort, or pause as inconclusive ([analysis documentation](https://github.com/argoproj/argo-rollouts/blob/f2c5c2b51ff5ef0b071fcf9883614907aa055c52/docs/features/analysis.md)).

**Boundary.** Argo executes the submitted `Rollout` and referenced analysis configuration. A caller with Kubernetes write authority can change the image, canary steps, and analysis references. Metric providers evaluate the configured success and failure conditions, while third-party metric plugins extend the controller ([metric-plugin documentation](https://github.com/argoproj/argo-rollouts/blob/f2c5c2b51ff5ef0b071fcf9883614907aa055c52/docs/analysis/plugins.md)).

**SafeLane relationship.** Argo is the execution substrate, not a component to reproduce. SafeLane's extra work happens around it: resolve and freeze the candidate and accumulated change, explain deployment hazards before mutation, obtain approval for one exact Release Patch, recheck material facts, remain attached to the rollout, and record proof. The reviewed Argo documentation does not define that surrounding source/artifact-to-approval workflow.

### 2. `argoproj-labs/rollouts-plugin-metric-ai`

This is the closest direct prior art for the phrase “agentic rollout.” The Argo metric-provider plugin delegates analysis to an A2A agent. Its request includes the namespace, rollout name, stable/canary label selectors, optional prompt context, and optional GitHub repository information; the response includes structured fields such as `promote` and `confidence` ([A2A client](https://github.com/argoproj-labs/rollouts-plugin-metric-ai/blob/2ed36703f382a714bc111c02e037c3cb0cb93bb7/internal/plugin/a2a.go), [agent-only analysis path](https://github.com/argoproj-labs/rollouts-plugin-metric-ai/blob/2ed36703f382a714bc111c02e037c3cb0cb93bb7/internal/plugin/ai_mode.go)). The plugin maps `promote: true` to a successful Argo measurement and `false` to a failed one ([plugin `Run`](https://github.com/argoproj-labs/rollouts-plugin-metric-ai/blob/2ed36703f382a714bc111c02e037c3cb0cb93bb7/internal/plugin/plugin.go)).

The documented flow deploys a canary, routes limited traffic, asks the agent to collect Kubernetes evidence, decides promotion or rollback, and can start source remediation ([architecture](https://github.com/argoproj-labs/rollouts-plugin-metric-ai/blob/2ed36703f382a714bc111c02e037c3cb0cb93bb7/ARCHITECTURE.md)). Argo Rollouts also lists the AI plugin in its metric-plugin catalogue ([Argo plugin catalogue](https://github.com/argoproj/argo-rollouts/blob/f2c5c2b51ff5ef0b071fcf9883614907aa055c52/docs/analysis/plugins.md)).

**Boundary.** The remote agent's result is trusted as the metric outcome. In the inspected source, the request is addressed to a configured `agentUrl`, the wrapper does not add request or response signatures, and a parsed `promote: true` becomes success. The request identifies rollout and pod context by names and selectors, not a complete deployed-baseline-to-candidate source delta and immutable Artifact identity ([A2A client](https://github.com/argoproj-labs/rollouts-plugin-metric-ai/blob/2ed36703f382a714bc111c02e037c3cb0cb93bb7/internal/plugin/a2a.go), [plugin implementation](https://github.com/argoproj-labs/rollouts-plugin-metric-ai/blob/2ed36703f382a714bc111c02e037c3cb0cb93bb7/internal/plugin/plugin.go)).

**SafeLane relationship.** The plugin owns AI-assisted runtime analysis. SafeLane's Plan 41 wedge is earlier and broader in release context: validate eligibility, freeze the exact Release Delta, make a grounded recommendation, show the concrete lane, obtain one exact approval, and then coordinate the application's existing background analysis through completion and proof. SafeLane should not put a second AI decision inside Argo's health-analysis loop.

### 3. `kdubois/kubernetes-aiops-agent`

This project is the reference backend used by the Argo AI metric plugin. It gathers stable/canary logs, events, pod state, and metrics; an LLM-backed `AnalysisAgent` returns a typed result with promotion, confidence, analysis, root cause, and remediation fields ([analysis prompt and output](https://github.com/kdubois/kubernetes-aiops-agent/blob/148b446d0900349032f428df4400f48addb06e2f/src/main/java/dev/kevindubois/rollout/agent/agents/AnalysisAgent.java), [workflow](https://github.com/kdubois/kubernetes-aiops-agent/blob/148b446d0900349032f428df4400f48addb06e2f/src/main/java/dev/kevindubois/rollout/agent/workflow/KubernetesWorkflow.java)). It also documents automatic root-cause analysis and asynchronous GitHub issue or pull-request creation ([README](https://github.com/kdubois/kubernetes-aiops-agent/blob/148b446d0900349032f428df4400f48addb06e2f/README.md)).

**Boundary.** The promotion Boolean comes from the LLM workflow. A second scoring agent evaluates the response, but that evaluation is also model output ([scoring agent](https://github.com/kdubois/kubernetes-aiops-agent/blob/148b446d0900349032f428df4400f48addb06e2f/src/main/java/dev/kevindubois/rollout/agent/agents/ScoringAgent.java)). The HTTP resource returns the result and starts remediation when required ([A2A resource](https://github.com/kdubois/kubernetes-aiops-agent/blob/148b446d0900349032f428df4400f48addb06e2f/src/main/java/dev/kevindubois/rollout/agent/a2a/KubernetesAgentResource.java)). Its checked-in Kubernetes RBAC reads workloads, Rollouts, AnalysisRuns, templates, logs, events, and metrics and allows pod execution, but contains no Kubernetes write verb; GitHub remediation separately needs a repository token ([RBAC](https://github.com/kdubois/kubernetes-aiops-agent/blob/148b446d0900349032f428df4400f48addb06e2f/deployment/rbac.yaml), [prerequisites](https://github.com/kdubois/kubernetes-aiops-agent/blob/148b446d0900349032f428df4400f48addb06e2f/README.md#prerequisites)).

**SafeLane relationship.** This is strong overlap for live diagnosis and promote/rollback judgment, but not for SafeLane's complete release transaction. Its inputs and outputs do not provide the reviewed Plan 41 contract of exact Artifact resolution, accumulated source change, explicit target and impact, pre-release recommendation, approval-bound patch, attached control, and compact proof.

### 4. kagent

kagent is a Kubernetes-native framework for agents rather than a dedicated release product. It represents agents, model configuration, and MCP tool servers as Kubernetes resources, serves agents over A2A, and includes integrations for Kubernetes, Argo, Prometheus, Grafana, Helm, Istio, and other operational systems ([README](https://github.com/kagent-dev/kagent/blob/7beafb75b4ec9f9a69cc67a9c6ed2cc57904a2cd/README.md), [architecture guide](https://github.com/kagent-dev/kagent/blob/7beafb75b4ec9f9a69cc67a9c6ed2cc57904a2cd/docs/architecture/README.md)).

**Boundary.** Agents act through configured tools and credentials. kagent can pause an agent before a named tool call and require the client to approve or reject that exact call ([human-in-the-loop documentation](https://github.com/kagent-dev/kagent/blob/7beafb75b4ec9f9a69cc67a9c6ed2cc57904a2cd/docs/architecture/human-in-the-loop.md)). Its controller writer role can create, update, patch, and delete broad Kubernetes resource groups unless deployments constrain it, making RBAC design central to its authority boundary ([writer RBAC template](https://github.com/kagent-dev/kagent/blob/7beafb75b4ec9f9a69cc67a9c6ed2cc57904a2cd/helm/kagent/templates/rbac/writer-role.yaml)).

**SafeLane relationship.** kagent could host or call release tooling, but it does not itself define SafeLane's release-specific evidence model or lifecycle. Generic agent planning, sessions, Kubernetes tools, and per-tool approval are platform capabilities; SafeLane must remain valuable as the opinionated Release Delta → recommendation → exact approval → Argo execution → proof workflow rather than becoming another general agent framework.

### 5. Stakpak Agent

Stakpak positions itself as an autonomous DevOps agent that can generate infrastructure, debug Kubernetes, configure CI/CD, and automate deployments. Its repository describes continuous Autopilot operation, profiles for tools and auto-approval, and rulebooks for reusable operational procedures ([README](https://github.com/stakpak/agent/blob/760cd2b5984d29c2d513bb15ca33e995fae45f17/README.md)). This is meaningful prior art for the category claim “an autonomous agent deploys and operates software safely.”

Its safety surface includes secret substitution and Warden, described as a network-level firewall for destructive operations. The open repository's wrapper downloads the Warden executable from a Stakpak release endpoint and runs the agent container through it; the policy-enforcement implementation is not present in the reviewed repository, so this note does not infer guarantees beyond the published interface ([Warden wrapper](https://github.com/stakpak/agent/blob/760cd2b5984d29c2d513bb15ca33e995fae45f17/cli/src/commands/warden.rs), [Warden configuration](https://github.com/stakpak/agent/blob/760cd2b5984d29c2d513bb15ca33e995fae45f17/cli/src/config/warden.rs)).

**Boundary.** Stakpak constrains an agent through tool permissions, approval configuration, container mounts, secret handling, and network rules. Those are general agent-runtime controls rather than the application-specific release record reviewed in Plan 41.

**SafeLane relationship.** Stakpak covers the broad autonomous-DevOps narrative and is therefore a real competitor for attention. The reviewed source does not expose SafeLane's specific combination: compare the deployed baseline with one immutable candidate, ground a deployment recommendation in the complete delta and existing health checks, bind one approval to a narrow Argo patch, remain attached to execution, and retain compact proof. That is the comparison to make; independent permits and admission control are no longer SafeLane claims.

## Product claims invalidated by the prior art

1. **“AI evaluates a canary and decides whether to promote or roll back.” — Already built.** The Argo AI metric plugin and Kubernetes AIOps agent implement this directly.
2. **“An agent diagnoses a bad deployment and starts remediation.” — Already built.** The Kubernetes AIOps agent creates issues or pull requests, while kagent and Stakpak provide broader operational-agent capabilities.
3. **“Autonomous progressive delivery for coding agents.” — A category headline, not sufficient differentiation by itself.** Argo owns progressive delivery, the metric-AI pair demonstrates agentic analysis, and kagent and Stakpak cover general agentic operations.

SafeLane remains coherent only if the product consistently demonstrates its narrower workflow:

1. Resolve an immutable candidate Artifact and its successful build.
2. Identify the deployed baseline and assess the complete accumulated change.
3. Keep eligibility separate from semantic deployment judgment.
4. Ground hazards in frozen evidence and connect them to the application's configured health analysis.
5. Show a concrete release sequence, not only a lane label.
6. Obtain one explicit approval bound to the candidate, target, patch, analysis, and assessment.
7. Recheck material facts and patch only the selected image and configured canary steps.
8. Stay attached for status, hold, continue, stop, terminal outcome, and stored proof.

If SafeLane degrades into a prompt that edits a Rollout or an AI metric that watches a canary, the existing projects already own that story.

## Risks revealed by the prior art

- **Category collapse:** “Agentic rollout” alone describes existing work. Product materials must make the frozen release context, one approval, narrow mutation, and attached proof visible.
- **Duplicate runtime judgment:** Adding another AI promotion decision inside Argo would overlap the metric-AI pair and conflict with Plan 41's decision to use the application's configured background analysis.
- **Artifact ambiguity:** Names, tags, and selectors are useful operational handles but insufficient for SafeLane's promise that assessment, approval, and deployment concern the same built Artifact.
- **Approval drift:** Generic per-tool approval is not the same as approval of the candidate, target, patch, analysis, and assessment as one frozen release transaction.
- **Framework-shaped scope creep:** General agent orchestration, memory, MCP/A2A plumbing, remediation, policy engines, and admission control are established platform categories and are outside the Plan 41 POC.
- **Execution ownership confusion:** Argo must remain the authority for health evaluation and rollback. SafeLane coordinates the approved release and records its outcome.
- **Extension maturity:** Argo's metric-plugin mechanism is documented as alpha. SafeLane does not need that dependency for Plan 41 because it reuses the application's existing background analysis rather than installing an AI metric plugin ([Argo metric-plugin documentation](https://github.com/argoproj/argo-rollouts/blob/f2c5c2b51ff5ef0b071fcf9883614907aa055c52/docs/analysis/plugins.md)).

## Bottom line for SafeLane

The value question is not “can an agent make an Argo rollout smarter?” Existing projects show that it can. SafeLane's test is more concrete:

> Does freezing the exact release, explaining deployment-specific hazards, asking once, constraining the approved mutation, staying with Argo through the outcome, and preserving proof make an agent-driven production release easier to trust and operate?

That is a distinct integration of existing capabilities in the reviewed source set, not proof of an uncontested category or of market demand. The POC should make every part of that transaction observable. If it cannot, SafeLane collapses back into prior art.
