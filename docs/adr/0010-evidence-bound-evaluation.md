# ADR 0010: Evidence-bound continuous-learning evaluation

- Status: accepted
- Date: 2026-08-05

## Decision

Continuous learning is evaluated by later-task outcomes, not by stored-memory
or retrieval counts. The initial evaluation protocol measures complete capture,
false memory, repeated correction, compaction drift, retrieval cost, and paired
task outcomes. Cross-device and cross-Agent reliability remains a separate
acceptance surface.

Legacy context-journal cards and their referenced rollouts are frozen as a
local-only regression corpus. The corpus stores exact cards and index bytes as
content-addressed blobs and points to source snapshots already held by the
evidence ledger. Absolute source paths are replaced by hashes. Raw-byte capture
and normalized-event projection are reported separately because a parser gap
does not erase a preserved source snapshot, and a parsed record does not prove
complete byte capture.

A deterministic local review pack samples only candidates and compaction
checkpoints whose episodes overlap the frozen corpus. Separate strata cover
strong user evidence, stable repetition, compaction correction, semantic
conflict, untrusted negative controls, and the four reviewable continuity
states. The pack binds corpus, candidate, episode, and ledger-prefix identities
but carries no label and grants no review or promotion state.
Candidate samples retain only corpus-overlapping observations. Compaction
samples retain complete status counts and a bounded deterministic projection
of checks, while exact event IDs continue to point to the local evidence source.

Each evaluation case references immutable evidence. Directly measurable values
are checked against source-snapshot, retrieval, injection, adoption, tool-result,
or file-change events. Interpretive measurements require a separate local
append-only human source. False-memory cases use a per-case attestation whose
payload repeats the exact measurement and binds case, Agent, attestor, time, and
reason. Compaction drift instead uses one complete human-reviewed pack over the
frozen subject universe, sealed before any retained detector generation covers
those subjects. Counting and paired-result cases are derived from replayable
receipts rather than aggregate attestations.

Metric calculation is pure and never certifies a release. A persisted component
run may be release-ready only for its bounded diagnostic after every reference
resolves, the optional corpus and portable repository verify, at least one
threshold is configured, and every gate passes. Continuous-learning release
readiness additionally requires the fixed policy, independent capture inventory,
complete sealed paired-trial population, supervised execution receipts, prior
compaction ground truth, and blind built-in oracle controls
adopted in ADR 0016. Both profiles remain
measurement-only; release readiness describes the exact evaluated population,
not general product efficacy.
Missing denominators are `not_evaluable`. Inputs, reports, and attestations
remain local-only and are anchored in the evidence ledger.

## Alternatives

- Importing all legacy cards as active memory was rejected because summaries
  contain unsupported, duplicated, stale, and incomplete claims.
- Treating every parser gap as raw data loss was rejected because exact source
  bytes may still be preserved.
- Accepting a label and an unverified attestation hash in the same input was
  rejected because it does not bind the label to an independently recorded
  event.
- Treating retrieval as evidence of benefit was rejected because selection,
  delivery, adoption, and task outcome are distinct observations.
- Passing empty samples was rejected because an absent denominator provides no
  evidence for a quality claim.
- Sampling every candidate generation without a corpus-membership filter was
  rejected because unrelated local tasks would contaminate a historical
  regression baseline.

## Consequences

- Evaluation authoring requires explicit evidence references and, for
  interpreted cases, a prior attestation event.
- Reports can distinguish integrity, measurement, and release-gate failures.
- Historical reports remain reproducible from local evidence without exposing
  personal transcripts in Git.
- The first baseline is conservative: unsupported categories remain
  unevaluable instead of receiving optimistic defaults.
