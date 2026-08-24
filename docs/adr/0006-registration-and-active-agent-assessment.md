# Registration and active-agent assessment

SafeLane uses deterministic registration for discovered deployment facts and
product-default release settings, while the user's active Claude or Codex
session assesses deployment hazards from a frozen Release Delta. SafeLane
validates grounding, maps the assessment to a configured lane, binds one human
approval to the exact Release Patch, and remains attached while Argo executes
health analysis and rollback. This replaces generated setup plans, SafeLane-
authored health analysis, nested model execution, deterministic semantic
heuristics, and guarded fallback. It supersedes ADR-0005 and the assessor/fallback
parts of ADR-0003; eligibility remains separate from risk and risk still maps
only to a configured lane.
