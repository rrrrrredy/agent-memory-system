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
`task-attempt-receipt/v1alpha2` records as their canonical replay record. A receipt
binds:

- a stable task and attempt identity;
- hashes and local blob references for the exact task specification,
  acceptance criteria, execution configuration, and current system artifact;
- a system-under-test manifest whose local blobs bind the actual system prompt,
  tool registry, harness, and Agent adapter;
- one Agent, semantic key, and baseline or memory condition;
- an exact, complete ledger window;
- every observed result and user message in that window;
- causal ancestry from task start or exact memory injection;
- an identified, versioned oracle verdict with an optional registry-entry hash;
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
every fixed metric gate, efficacy prerequisite, and positive sample minimum, and remains measurement-only.
ADR 0016 adds the atomic release gate: it binds the versioned fixed policy,
deterministically rebuilds independent capture and sealed paired-trial
populations, requires exact measured-subject coverage, full promoted-memory
projection, prior compaction ground truth, task-artifact blobs, and supervised
execution, then reruns the blind built-in `evidence-score/v1` oracle. Controlled
efficacy trials use `trial preregister`, `attempt execute`, and `attempt finalize`;
per-attempt preregistration, manual observation, and `attempt record` remain
component compatibility paths. Native checkers remain diagnostic. Self-reported
trust flags, adapter allowlists, source strings, signatures, or submitted
population hashes do not establish those properties.

## Consequences

- An observed zero correction count requires a closed, fully labeled window;
  missing user-message evidence cannot silently become zero.
- Baseline and memory results with different task contracts cannot form a
  pair.
- Self-declared human or harness identities remain unauthenticated, and an
  oracle verdict remains a policy judgment. Registry-bound replay makes harness
  output reproducible rather than objectively true.
- Passing every continuous-learning metric gate and efficacy prerequisite applies only to the exact bound
  population and cannot be reported as general product efficacy.
- Provider-private events or reasoning that never reach the local runtime
  remain outside the evidence boundary and must be reported as unavailable.
