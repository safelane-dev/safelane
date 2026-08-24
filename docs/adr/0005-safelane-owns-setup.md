---
status: superseded by ADR-0006
---

# SafeLane owns setup

SafeLane is the sole setup authority: it discovers mechanical facts, owns product safety rules, validates Semantic Findings, compiles supported assertion intents into executable Runtime Assertions, creates an immutable Setup Plan, applies the approved plan, and reconciles the result. Claude, Codex, or another agent is a replaceable semantic analyst that may contribute evidence-backed application risk paths and behavioral assertion intents, but it never edits a SafeLane baseline or authors operational configuration; deterministic setup uses the same compiler with conservative internal findings.
