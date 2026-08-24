# Risk maps to a configured lane

Eligibility and deployment risk answer different questions: evidence decides
whether an exact candidate may be assessed at all, while the grounded active-
agent assessment judges deployment risk. SafeLane validates that assessment and
maps Low, Medium, or High through the Release Settings to a lane that already
exists; neither the agent nor caller supplies rollout weights. ADR-0006 replaces
this decision's former deterministic heuristic floor and Guarded fallback with
one correction attempt followed by a Wait recommendation.
