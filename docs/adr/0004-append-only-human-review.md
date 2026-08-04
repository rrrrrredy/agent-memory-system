# ADR 0004: Append-only human review

- Status: accepted
- Date: 2026-08-04

## Decision

Candidate validation uses a local append-only review ledger. Each transition
binds to a candidate ID and content hash, declares its expected previous state,
and records a human reviewer attestation, confirmed scope, evidence basis, and
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
- The human reviewer field is an attestation supplied by the caller; an
  integration must still obtain the user's actual decision.
- Promotion, redaction, secret scanning, supersession, and revocation remain a
  separate state transition and cannot be inferred from `validated`.
