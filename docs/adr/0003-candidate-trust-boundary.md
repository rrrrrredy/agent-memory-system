# ADR 0003: Candidate trust and review boundary

- Status: accepted
- Date: 2026-08-04

## Decision

Candidate generation is a deterministic, local-only derivation. It may classify
a candidate as `review_ready`, but it cannot validate, promote, inject,
synchronize, or execute the candidate.

The initial extractor accepts user-origin instructions and corrections. It does
not treat assistant agreement, a model-authored summary, or tool output as
independent proof. Ordinary task goals are excluded unless an explicit remember
instruction or a later user correction supplies stronger evidence.

Exact normalized duplicates accumulate provenance. Opposing polarity under the
same deterministic semantic key is quarantined on both sides. Ambiguity and
unrecognized semantic conflict remain review responsibilities; a parser or
detector failure never defaults to promotion.

Every candidate starts with unconfirmed scope. Any later review transition must
bind to the candidate's content hash and record the reviewer, decision, scope,
and expected prior state in an append-only decision ledger.

## Consequences

- Candidate counts are not a learning-success metric.
- `review_ready` is intentionally weaker than `validated`.
- Stable repetition can prioritize review but cannot bypass human scope
  confirmation.
- Changes to `AGENTS.md`, Skills, hooks, plugins, or global rules require a
  separate explicit user approval even after a related memory is promoted.
- Candidate files can contain raw user text and remain in the local evidence
  zone.
