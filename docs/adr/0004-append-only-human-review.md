# ADR 0004: Append-only review attestations

- Status: accepted
- Date: 2026-08-04

## Decision

Candidate validation uses a local append-only review ledger. Each transition
binds to a candidate ID and content hash, declares its expected previous state,
and records a caller review attestation, confirmed scope, evidence basis, and
reason.

Review requests fail closed when the candidate generation, referenced evidence,
review chain, expected state, or complete conflict group cannot be verified.
Opposing candidates are resolved atomically; no member can be validated while
an omitted opposing member remains silently active.

The review ledger uses a hash chain and a local exclusive writer lock. It is
local evidence, not portable promoted memory. A validated transition does not
create a memory revision and cannot trigger retrieval, synchronization, or a
change to an Agent rule surface.

## Consequences

- Review decisions are auditable, reversible through later events, and never
  rewritten in place.
- Replaying events produces the current state without SQLite or another mutable
  canonical database.
- Content changes require a new review because decisions bind to the candidate
  content hash.
- A new candidate generation also requires explicit review; old validation
  cannot conceal a conflict discovered by later evidence.
- Candidate observations are checked against their source episodes and verified
  user-message ledger records; complete referenced result payloads are also
  hash-verified before validation.
- `caller_attestation` is supplied by the caller and does not authenticate a
  human identity; an integration must still obtain the user's actual decision.
- Automated regression fixtures use `synthetic_test`. The old `human` value is
  retained only so existing records remain verifiable.
- Promotion, redaction, secret scanning, supersession, and revocation remain a
  separate state transition and cannot be inferred from `validated`.
