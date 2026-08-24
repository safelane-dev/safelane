# Stakpak workflow research and possible SafeLane Phase 2 adaptations

Date: 2026-08-24
Status: Research note, not part of the SafeLane POC scope
Question: What can SafeLane learn from Stakpak's product workflow, especially profiles, without expanding the locked POC?

## Executive conclusion

The useful lesson is not "SafeLane should copy Stakpak profiles." Stakpak and SafeLane operate at different levels: Stakpak is a general DevOps agent with many tools, models, knowledge sources, invocation surfaces, and long-running jobs; SafeLane is deliberately a narrow release coordinator around an already configured Argo Rollout.

The transferable idea is a **named, validated behavior envelope selected by an invocation**. In Stakpak, a profile can determine model/provider settings, allowed tools, auto-approval, rulebook selection, system prompt, turn limit, and Warden behavior. Schedules and messaging channels reference the profile but keep their delivery/runtime wiring elsewhere. This separates "how this agent may behave" from "what woke it up and where results go." [Configure Stakpak](https://stakpak.gitbook.io/docs/get-started/configure-stakpak), [current CLI configuration guide](https://github.com/stakpak/agent/blob/760cd2b5984d29c2d513bb15ca33e995fae45f17/cli/README.md), [profile implementation](https://github.com/stakpak/agent/blob/760cd2b5984d29c2d513bb15ca33e995fae45f17/cli/src/config/profile.rs)

For SafeLane Phase 2, the strongest adaptation is **Verified Release Context**, not a profile system: small, source-cited Application/Environment facts that reduce repeated material assessment questions without storing conversations or authorizing releases. A profile-like concept becomes useful only after SafeLane has another real invocation surface such as scheduled observation or Slack. At that point, test two built-in operating modes—`release` and `watch`—before designing user-configurable profiles.

The other important Stakpak lessons are:

1. onboarding should produce a usable environment model, not merely credentials;
2. planning and mutation are visibly separated;
3. durable knowledge, procedural rules, and execution history are separate products with different trust levels;
4. an invocation surface chooses behavior but does not define it inline;
5. deterministic enforcement remains outside the model; and
6. long-running work needs explicit status, recovery, and audit affordances.

These are Phase 2 design inputs only. None should expand the definition of done or out-of-scope boundary in [the locked engineering plan](../plans/41.md).

## Sources and method

This note uses only first-party material:

- the current [Stakpak GitBook documentation](https://stakpak.gitbook.io/docs);
- the public [stakpak/agent repository](https://github.com/stakpak/agent), pinned here to commit [`760cd2b`](https://github.com/stakpak/agent/tree/760cd2b5984d29c2d513bb15ca33e995fae45f17); and
- first-party repositories linked by those docs, such as [stakpak/paks](https://github.com/stakpak/paks).

The research read the product entrypoints plus installation, configuration, profiles, initialization, planning, sessions, memory, rulebooks, knowledge hierarchy, integrations, secrets, Warden, and Autopilot documentation. Repository evidence was used for the current profile and approval structures, not to infer undocumented product promises.

### Documentation drift caveat

The GitBook and repository are changing quickly and are not perfectly synchronized. For example:

- product docs describe both interactive configuration and direct login/API-key flows;
- the docs use `-a` for async mode while the repository getting-started guide also shows `--async` and `--print`;
- the GitBook describes profile switching with `Ctrl+K`, while slash-command docs also expose `/profiles`;
- older material emphasizes the interactive agent, while the current repository prominently leads with `/init` followed by `autopilot up`;
- the GitBook describes cloud sessions and memory, while the edition comparison distinguishes local OSS storage from shared Cloud/Enterprise storage.

These are not resolved by guessing. The findings below state the common product shape and cite the exact surface supporting each claim. Command spelling and edition-specific storage should be reverified before implementation. [Using Stakpak](https://stakpak.gitbook.io/docs/get-started/using-stakpak), [Slash Commands](https://stakpak.gitbook.io/docs/how-it-works/slash-commands), [OSS vs Cloud vs Enterprise](https://stakpak.gitbook.io/docs/get-started/oss-vs-cloud-vs-enterprise), [repository getting-started guide](https://github.com/stakpak/agent/blob/760cd2b5984d29c2d513bb15ca33e995fae45f17/GETTING-STARTED.md)

## Verified product workflow

Everything in this section is a verified fact from a cited first-party source. Interpretations and SafeLane proposals appear later.

### 1. Install and choose an operating edition

Stakpak is distributed as a CLI. The first-party material documents a one-line installer for Linux/macOS, Homebrew, downloadable binaries including Windows, Docker, and building from source. Windows setup uses a release archive followed by `stakpak.exe login --api-key ...` and `stakpak.exe account`. [Install Stakpak](https://stakpak.gitbook.io/docs/get-started/install-stakpak), [Windows installation](https://stakpak.gitbook.io/docs/get-started/install-stakpak/installation-on-windows), [repository README](https://github.com/stakpak/agent/tree/760cd2b5984d29c2d513bb15ca33e995fae45f17#installation)

Configuration branches into:

- Stakpak Cloud, where a browser-assisted flow signs in and supplies a Stakpak API key; or
- the open-source/BYOK path, where the user selects Anthropic, Google, OpenAI, hybrid smart/eco providers, or an OpenAI-compatible model endpoint.

The OSS edition can run in the user's environment using the user's model key. The docs say its only Stakpak API dependency is unauthenticated retrieval of public rulebooks. Cloud and Enterprise add shared sessions, rulebooks, and memory; the edition table also distinguishes local, cloud, and on-premises data storage. [Configure Stakpak](https://stakpak.gitbook.io/docs/get-started/configure-stakpak), [OSS vs Cloud vs Enterprise](https://stakpak.gitbook.io/docs/get-started/oss-vs-cloud-vs-enterprise)

### 2. Profiles save agent configuration

A user creates profiles through `stakpak config`, names a profile, and completes configuration. A user can switch profiles from the interactive interface with `Ctrl+K`; `/profiles` is also listed as a configuration command. The underlying file is `~/.stakpak/config.toml`, and `stakpak config sample` prints an example. [Profiles](https://stakpak.gitbook.io/docs/how-it-works/profiles), [Slash Commands](https://stakpak.gitbook.io/docs/how-it-works/slash-commands)

The current documented sample supports a special `profiles.all` default plus named profiles. A profile may specify provider/API settings, model, allowed tools, auto-approved tools, rulebook include/exclude patterns and tags, system prompt, maximum turns, and Warden configuration. The code's `ProfileConfig` matches this general shape and validates `max_turns` and system-prompt size. [Configure Stakpak](https://stakpak.gitbook.io/docs/get-started/configure-stakpak), [profile implementation](https://github.com/stakpak/agent/blob/760cd2b5984d29c2d513bb15ca33e995fae45f17/cli/src/config/profile.rs)

The sample's production profile restricts tools to read-only operations while its development profile permits file and command tools and expands auto-approval. This is first-party demonstration material, not a guarantee that a name such as `production` has built-in semantics. [Configure Stakpak](https://stakpak.gitbook.io/docs/get-started/configure-stakpak)

### 3. Initialize understanding of the environment

Inside an interactive session, `/init` scans reachable infrastructure including cloud accounts, clusters, and configuration. The docs say it detects integrations, analyzes permissions, stateful services, drift, and exposure, recommends schedules and guardrails, creates `Apps.md`, and then offers to configure Autopilot. [/Init](https://stakpak.gitbook.io/docs/how-it-works/init)

The current first-party quick start reduces that path to three steps: install Stakpak, run `stakpak init` to understand applications and the technology stack, then start `stakpak autopilot up`. [repository README](https://github.com/stakpak/agent/tree/760cd2b5984d29c2d513bb15ca33e995fae45f17#try-stakpak-now)

### 4. Invoke it through several surfaces

The normal interactive entrypoint is `stakpak` from the working directory. The TUI provides chat, progress, shortcuts, and an approval interface. Async mode accepts a prompt and a configurable step limit; the docs state a default of 50 steps. Stakpak can also work through SSH/SFTP, as an MCP server, through ACP-compatible editors such as Zed, and through messaging channels used by Autopilot. [Using Stakpak](https://stakpak.gitbook.io/docs/get-started/using-stakpak), [repository getting-started guide](https://github.com/stakpak/agent/blob/760cd2b5984d29c2d513bb15ca33e995fae45f17/GETTING-STARTED.md), [Agent Client Protocol](https://stakpak.gitbook.io/docs/how-it-works/agent-client-protocol-acp), [Autopilot](https://stakpak.gitbook.io/docs/how-it-works/autopilot)

Shell mode is entered with `$` and exists for commands that require real-time input such as `sudo`, passwords, confirmations, or interactive scripts. [Shell Mode](https://stakpak.gitbook.io/docs/how-it-works/shell-mode)

### 5. Plan, review, and approve before execution

`/plan` enters Plan Mode. The product description is explicit: the agent outlines its intended steps before execution; the user can review, adjust, and approve the plan. [Plan Mode](https://stakpak.gitbook.io/docs/how-it-works/plan-mode)

Tool execution has a separate approval mechanism. Profiles can limit `allowed_tools` and list tools that are automatically approved. The current agent-core implementation resolves each proposed tool call through an approval policy with `approve`, `deny`, or `ask` outcomes; it also supports resolving multiple pending calls in one command and preserves dispatch order. [current CLI configuration guide](https://github.com/stakpak/agent/blob/760cd2b5984d29c2d513bb15ca33e995fae45f17/cli/README.md), [approval state machine](https://github.com/stakpak/agent/blob/760cd2b5984d29c2d513bb15ca33e995fae45f17/libs/agent-core/src/approval.rs)

The repository describes bulk message approval as a security/usability feature. File modifications are backed up for recovery, and `/rollback` finds changes from the current session, lets the user choose which changes to undo, asks for confirmation, and reverses them where possible. [repository README](https://github.com/stakpak/agent/tree/760cd2b5984d29c2d513bb15ca33e995fae45f17), [Slash Commands](https://stakpak.gitbook.io/docs/how-it-works/slash-commands)

### 6. Enforce guardrails outside the model

Warden is described as a deterministic policy enforcer which inspects agent actions and blocks destructive or unauthorized operations before they reach the environment. It can run Stakpak inside a sandbox with explicitly mounted configuration and cloud credentials. [Warden Guardrails](https://stakpak.gitbook.io/docs/how-it-works/warden-guardrails)

Secret handling is a separate runtime mechanism: Stakpak says it substitutes secret values so the agent can read, compare, or write them without exposing the original values in logs or memory. Privacy mode redacts additional sensitive identifiers. [Handling Secrets](https://stakpak.gitbook.io/docs/how-it-works/handling-secrets)

### 7. Keep procedure, learned knowledge, and history distinct

Stakpak documents four knowledge sources in an explicit order:

1. organization-owned Rulebooks;
2. community Paks/Agent Skills;
3. current official external documentation; and
4. persistent operational Memory.

The product assigns different owners and trust levels to these sources rather than flattening all context into one prompt. [Knowledge Sources](https://stakpak.gitbook.io/docs/how-it-works/knowledge-sources)

Rulebooks are Markdown SOPs for repeatable organizational procedures such as deployments, upgrades, and migrations. Stakpak offers official rulebooks and organization-authored rulebooks. Users can select them for one session in the TUI or filter them per profile by URI patterns and tags. [Rulebooks](https://stakpak.gitbook.io/docs/how-it-works/rulebooks), [How to Write a Rulebook](https://stakpak.gitbook.io/docs/how-it-works/rulebooks/how-to-write-a-rulebook)

Paks are reusable community Agent Skills managed by a separate CLI-first package manager. The first-party Paks repository supports creating, validating, installing, publishing, searching, listing, and removing skills across multiple agent products and global/project scopes. [Paks documentation](https://stakpak.gitbook.io/docs/how-it-works/paks), [stakpak/paks](https://github.com/stakpak/paks)

Knowledge Store is the persistent operational memory layer. It can save, search, read, update, and remove infrastructure discoveries, troubleshooting notes, procedures, decisions, and project-specific knowledge across sessions through `stakpak ak`. `/memorize` extracts the current conversation into memory, while the repository's CLI guide documents a retrospective workflow that converts past sessions into cited knowledge entries and skips already-cited sessions. [Memory (Knowledge Store)](https://stakpak.gitbook.io/docs/how-it-works/memory-knoweldge-store), [current CLI configuration guide](https://github.com/stakpak/agent/blob/760cd2b5984d29c2d513bb15ca33e995fae45f17/cli/README.md)

Agent Sessions are the audit/history surface. They include full conversation history, key checkpoints and decisions, actions and their results, and a debugging trail. Users can access sessions on the web or with `/sessions`; `/resume`, `/new`, and `/summarize` provide lifecycle operations. The repository checkpoint format is versioned and stores messages, an optional run ID, and metadata. [Agent Sessions](https://stakpak.gitbook.io/docs/how-it-works/agent-sessions), [Slash Commands](https://stakpak.gitbook.io/docs/how-it-works/slash-commands), [checkpoint implementation](https://github.com/stakpak/agent/blob/760cd2b5984d29c2d513bb15ca33e995fae45f17/libs/agent-core/src/checkpoint.rs)

### 8. Run long-lived work through Autopilot

Autopilot runs scheduled or message-triggered agent work continuously, detects changes, automatically handles actions considered safe, and escalates other decisions through connected channels. It supports schedules plus Slack, Telegram, and Discord. The documented lifecycle commands include `up`, `down`, `status`, `logs`, `restart`, schedule/channel management, and `doctor` preflight diagnostics. [Autopilot](https://stakpak.gitbook.io/docs/how-it-works/autopilot)

The current configuration deliberately separates:

- `~/.stakpak/config.toml` for named behavior profiles and credentials; and
- `~/.stakpak/autopilot.toml` for schedules, messaging channels, notification routes, and service/runtime wiring.

A schedule or inbound channel references a profile by name. Notification routing is separately expressed as a transport plus destination. The current runtime resolves the caller's selected profile into per-run model, auto-approval, system prompt, and turn-limit overrides, then builds the run configuration. [current CLI configuration guide](https://github.com/stakpak/agent/blob/760cd2b5984d29c2d513bb15ca33e995fae45f17/cli/README.md)

### 9. Connect integrations by capability

The product's integration flow is: sign in to Stakpak Cloud, choose an integration, supply its required connection information, and select the capabilities agents may use. Integrations can be paused while retaining credentials, validated, resumed, or disconnected. The docs state that integrations use the Agent Auth Protocol for controlled connection to external tools. [Integrations](https://stakpak.gitbook.io/docs/how-it-works/integrations)

The documented Slack workflow additionally connects GitHub, installs Slack, provisions a GitHub Actions runner with a chosen repository and cloud identity, adds Stakpak to a channel, and then accepts natural-language operational requests there. [Slack Integration](https://stakpak.gitbook.io/docs/how-it-works/slack-integration)

## Inferences from the verified workflow

This section is interpretation, not a claim made directly by Stakpak.

### Profiles are capability contexts, not user personas

Although the introductory profile copy says "work, personal, and testing," the actual configuration surface is an authorization-and-behavior boundary. Provider credentials are only one element. Tool availability, auto-approval, Warden settings, procedural knowledge, prompt, and execution budget can change together.

That makes a profile closer to a named **operating posture** than a cosmetic preference bundle.

### Invocation and behavior are orthogonal

Autopilot's split shows a useful architectural seam:

```text
schedule / Slack / Telegram / Discord / local TUI
                         |
                         v
              selected named profile
                         |
                         v
             resolved per-run behavior
                         |
                         v
              execution + destination
```

An inbound Slack message does not need Slack-specific agent policy. A schedule does not need to duplicate its tool list and system prompt. Multiple entrypoints can select one validated posture, and notification destinations can change without redefining behavior.

### Planning approval and tool approval solve different problems

Plan Mode provides human comprehension and intent alignment at the task level. Tool-call policy provides deterministic enforcement at the action level. A reviewed plan does not imply that every arbitrary later action is safe, and auto-approving a read tool does not approve the overall operational goal.

SafeLane's already locked single-use release approval is stronger and more domain-specific than a generic list of auto-approved tools. Stakpak's layering supports keeping that design, not replacing it.

### Operational context needs provenance and lifecycle

Stakpak separates stable organizational procedure (Rulebooks), packaged community expertise (Paks), fresh official facts (documentation search), learned operational context (Memory), and execution records (Sessions). This implies that "agent memory" is not one storage bucket. Ownership, freshness, trust, mutability, and retention differ.

SafeLane already makes a similar distinction between registered configuration, frozen release evidence, user-provided facts, compact history, and detailed proof. Stakpak reinforces that direction.

### Initialization is a product moment

`/init` turns environment discovery into an intelligible model and offers the next useful action. It is not merely an authentication wizard. This likely reduces the gap between "CLI installed" and "agent can safely do something relevant."

SafeLane's locked deterministic registration has the same opportunity: conclude by explaining what Application/Environment was registered, what rollout and health analysis it found, what it will never mutate, and the first natural-language release request the user can make.

### Persistent agents require an operational control plane

Autopilot's `status`, `logs`, `doctor`, `restart`, schedules, and channels show that autonomy creates a second product surface: users need to understand whether the agent itself is healthy, what woke it, which behavior it selected, and where it reported results.

SafeLane Phase 1 deliberately stops at one active release workflow with status/hold/continue/stop. Any Phase 2 asynchronous or chat-channel work should first define equivalent observability and recovery, not merely add a webhook.

## Possible SafeLane Phase 2 adaptations

These proposals are deliberately outside the locked POC. They should be reconsidered only after the POC definition of done is met.

### Recommended adaptation: Verified Release Context

Stakpak separates persistent knowledge from procedural Rulebooks and execution Sessions. Its current retrospective workflow promotes selected session findings into small cited knowledge entries and skips sessions already cited. SafeLane can adapt this into a narrower store for reusable deployment facts. [current CLI configuration guide](https://github.com/stakpak/agent/blob/760cd2b5984d29c2d513bb15ca33e995fae45f17/cli/README.md), [Knowledge Sources](https://stakpak.gitbook.io/docs/how-it-works/knowledge-sources)

Example:

```yaml
fact: Production database migrations run in a separate job before rollout.
scope:
  application: payments-api
  environment: production
source:
  type: confirmed
  release: <internal-reference>
captured_at: 2026-08-24
last_verified_at: 2026-08-24
```

Rules:

1. Store reusable deployment facts, never conversations or assessment prose.
2. Scope every fact to an Application and optionally an Environment.
3. Require a source: immutable Release Proof, repository evidence, or explicit confirmation.
4. Live candidate and cluster evidence always outrank stored context.
5. Load a fact only when it is relevant to a credible hazard under assessment.
6. When a fact conflicts with current evidence or becomes stale, ask one focused question.
7. Explain the fact's source when it influences a recommendation.
8. Remembered context never authorizes mutation.

This directly improves SafeLane's defining experience: it can stop asking the same material question on every release without growing the normal history view or pretending that generic agent memory is trustworthy release evidence.

### Conditional experiment: operating modes, not configurable Profiles

Stakpak's profile model becomes relevant to SafeLane only when more than one invocation surface exists. Then test two built-in modes:

- **release**: assess, recommend, and coordinate the exact release after the existing explicit approval;
- **watch**: read status, health, history, and proof without loading or exposing any mutation path.

The trigger or channel selects the mode. Channel and destination remain separate routing configuration, matching Stakpak's behavior/wiring split. The mode does not live in `safelane.yml`, choose an Environment, select a lane, change assessment semantics, contain credentials, or vary the meaning of approval.

Borrow the separation, not Stakpak's configuration framework:

1. Keep the modes built in for the first experiment; add no inheritance or arbitrary capability lists.
2. Display the active mode plainly and record it in status/proof when relevant.
3. Enforce `watch` structurally by omitting controller credentials and mutation commands, not only through prompting.
4. Freeze `release` into the exact Release Delta when an external adapter initiated the workflow.
5. A mode may restrict behavior but can never grant permission beyond SafeLane's hard mutation boundary.
6. No mode can auto-approve a release; Phase 1's contextual, exact, single-use approval remains mandatory.
7. If a second invocation surface does not create real duplicated behavior, kill the abstraction.

### Do not overload existing lanes

A tempting design is to rename `fast`, `standard`, and `guarded` lanes as profiles or modes. That loses the main lesson. A lane is an execution strategy. A later operating mode only limits which existing operations an invocation surface can reach; it does not select, constrain, or prefer a lane.

Keeping them separate preserves the user's ability to see "25%, check, 50%, check, then full rollout" regardless of operating mode.

### Do not copy provider/API-key profiles into `safelane.yml`

Stakpak's profiles include model credentials because it is itself a general agent runtime. SafeLane's plan uses the active Claude session and intentionally removes model configuration and nested model execution. Reintroducing provider or API-key settings through Phase 2 profiles would reverse that decision and create secret-storage work unrelated to release coordination.

If SafeLane later becomes a standalone agent runtime, that is a separate architectural decision, not a natural extension of these operating modes.

### Mode-aware invocation adapters

After the POC, SafeLane could expose one narrow adapter at a time:

- scheduled **read-only release watch** that notices an in-progress rollout and reports status;
- GitHub-triggered **candidate ready** notification that prepares but does not approve a Release Delta; or
- Slack/Teams **release coordination** that can ask for status, hold, continue, or stop with the same identity and approval guarantees as the terminal.

Each adapter should pass only intent, authenticated actor, destination, and built-in mode into the existing release module. It should not reimplement assessment or Kubernetes mutation.

This applies Stakpak's invocation/behavior separation while respecting SafeLane's narrow core.

### Separate release procedure from learned history

If SafeLane later adds organization-specific guidance, use separate artifacts:

- **Release playbook**: reviewed, version-controlled procedure owned by the team;
- **Release memory**: compact, provenance-bearing findings learned from past releases;
- **Release proof**: immutable record of one executed release; and
- **external documentation evidence**: fresh, cited facts loaded for a named question.

Do not allow historical prose or retrieved instructions to authorize mutation. SafeLane's existing rule that repository and history content are untrusted evidence should remain dominant.

### Phase 2 onboarding polish

Without adding AI-generated configuration, registration can borrow the `/init` product shape:

1. discover mechanical facts;
2. let the user confirm the Application, Environment, Rollout, container, and impact;
3. write the deterministic configuration;
4. show a concise environment map;
5. explain the health analysis SafeLane will observe and the fields it may mutate; and
6. offer the exact first request: "Deploy payments-api to production."

This is primarily presentation over the already planned registration workflow.

### Phase 2 status and recovery

Before adding scheduled or channel-driven operation, add explicit answers to:

- Which operating mode and actor started this release?
- Is SafeLane assessing, awaiting approval, applying, paused, monitoring, completed, or failed?
- What evidence or human decision is it waiting for?
- Can the active session be safely resumed after process failure?
- Which state is authoritative: SafeLane's record or the observed Argo Rollout?
- Where are notifications being delivered?

Stakpak's sessions/checkpoints and Autopilot health commands suggest the product need. SafeLane should solve it using its domain records and Argo reconciliation rather than persisting a generic chat transcript.

## Ideas to reject or defer

The following Stakpak features are interesting but do not fit SafeLane's current shape:

- **Generic tool auto-approval.** SafeLane has a much smaller mutation boundary and exact release approval. Tool-level auto-approval would add ambiguity.
- **Full conversation history as audit.** The SafeLane plan correctly stores compact history plus detailed frozen proof, not tool traces and chat transcripts.
- **Generic rollback of file operations.** Argo owns rollout health and rollback; SafeLane's stop control and release proof are the relevant domain operations.
- **Agent-created schedules and guardrails during setup.** This conflicts with deterministic registration and the ban on AI-generated setup configuration.
- **General knowledge marketplace/Paks integration.** It would broaden the assessment into arbitrary skills and weaken the fixed release objective.
- **Provider/model profiles.** The POC deliberately uses the active Claude session.
- **Slack now.** Slack is explicitly out of scope, and adding it before the core workflow is finished would be scope creep.
- **Warden-style generic policy engine.** SafeLane should continue to enforce its two-field Rollout patch boundary directly in domain code.
- **Always-on self-healing.** SafeLane coordinates releases; it is not a general infrastructure remediation agent.

## Suggested Phase 2 experiments

After the POC is complete, test Verified Release Context first:

> A user-provided deployment fact from one release is stored with source and scope, loaded only when relevant to a later release, and either reduces one repeated assessment question or is rejected as stale—without changing approval or increasing the default history context.

Only after a scheduled or channel-driven adapter exists, test one thin operating-mode slice:

> From the same registered production environment, an invocation uses either built-in `release` mode or built-in read-only `watch` mode; SafeLane shows the resolved posture, exposes only that mode's existing operations, and records the mode in status and proof without changing lane semantics or approval binding.

Success criteria:

- both modes reuse one Application/Environment and the same release modules;
- invalid capability/approval combinations cannot be constructed;
- mode resolution is visible and frozen;
- read-only watch cannot reach any mutation path;
- interactive release still requires the exact final approval from the POC;
- changing the selected mode invalidates any pending recommendation;
- proof names the mode but still records the exact candidate, patch, lane steps, health analysis, actor, and outcome; and
- no model/provider configuration, Slack integration, policy language, or generic tool framework is introduced.

If this slice does not reduce duplication or enable a real repeated workflow, kill it. An operating-mode abstraction is not valuable merely because Stakpak has Profiles.

## Bottom line for the plan

Add this research note as a Phase 2 reference, not a new slice in the current engineering plan.

**Update, 2026-08-24:** three lessons were subsequently folded directly into [the plan](../plans/41.md) because each is presentation or hardening over already-planned work rather than a new subsystem — the `/init`-shaped registration readiness summary, named release states with a Rollout-wins authority rule, and a secret-exclusion boundary on frozen evidence and stored proof. The same review also simplified the plan's command surface, which is a plan-quality change rather than a Stakpak adaptation. Verified Release Context and operating modes remain Phase 2 as argued below.

The primary idea worth carrying forward is:

> Verified Release Context stores only small, source-cited deployment facts that reduce repeated assessment questions; live evidence outranks it and it never authorizes a release.

The conditional profile lesson is:

> If SafeLane later has multiple invocation surfaces, a built-in operating mode may constrain which existing release operations that surface can reach. It remains separate from Application, Environment, Lane, routing, and exact per-release approval.

That adaptation makes SafeLane more extensible without turning it into a general DevOps agent. The rest of Stakpak's surface is useful as evidence about onboarding, trust separation, and long-running operations—but mostly as a warning against widening the POC.
