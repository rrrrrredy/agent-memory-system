# ADR 0015: Evidence-bound task attempts and diagnostic profiles

- Status: accepted
- Date: 2026-08-05

## Context

Aggregate correction counts and before/after scores can be copied into an
evaluation attestation without proving which task executions produced them.
An adoption receipt also establishes only an attributed observation, not a
comparable task result. Either path could make an efficacy gate look complete
while hiding cherry-picked events, different task criteria, or unrelated user
corrections.

## Decision

Repeated-correction and paired-outcome gates use append-only
`task-attempt-receipt/v1alpha1` records as their canonical replay record. A receipt
binds:

- a stable task and attempt identity;
- hashes of the task specification, acceptance criteria, and execution
  configuration;
- one Agent, semantic key, and baseline or memory condition;
- an exact, complete ledger window;
- every observed result and user message in that window;
- causal ancestry from task start or exact memory injection;
- an identified, versioned oracle verdict; and
- a derived trial measurement.

A memory-conditioned attempt additionally binds verified retrieval, exact
injection, adoption, and the delivered memory revisions. A comparable pair
must match task, specification, criteria, configuration, semantic key, Agent,
and oracle identity/version.

The window starts with a structured task contract. Its source adapter and the
verdict source must match the declared oracle identity/version. Delivered
memory revisions must resolve through local promotion provenance to the same
semantic key as the task contract. Event labels and memory references use
sorted canonical arrays so equivalent inputs have one representation.

Receipts are local-only and have `measurement_only` authority. They cannot
review or promote a candidate, export memory, authorize a rule change, or
replace the raw evidence they reference. Evaluation verification replays each
referenced attempt and compares all derived measurements.

Evaluation inputs declare either a `component` profile or the
`continuous_learning` profile. The latter requires every quality category, all
twelve gates, and positive sample minimums, but remains a measurement-only
diagnostic. Every continuous-learning report records that efficacy is not
evaluable and cannot become release-ready while thresholds and population are
caller-selected and oracle judgments are not independently rerunnable.

A future efficacy policy must atomically bind a versioned policy to fixed
thresholds and eligibility rules, deterministically rebuild a population
manifest from frozen ledger and repository state, require exact measured-subject
coverage, and rerun keyed oracle checkers. Self-reported trust flags, adapter
allowlists, source strings, signatures, or submitted population hashes do not
establish those properties.

## Consequences

- An observed zero correction count requires a closed, fully labeled window;
  missing user-message evidence cannot silently become zero.
- Baseline and memory results with different task contracts cannot form a
  pair.
- Self-declared human or harness identities remain unauthenticated, and an
  oracle verdict remains a claim. The protocol makes that claim attributable
  and replayable rather than objectively true.
- Passing all twelve continuous-learning gates remains diagnostic and cannot
  be reported as product efficacy.
- Provider-private events or reasoning that never reach the local runtime
  remain outside the evidence boundary and must be reported as unavailable.
