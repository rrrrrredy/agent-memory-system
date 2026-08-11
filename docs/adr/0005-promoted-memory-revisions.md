# ADR 0005: Redacted promoted memory revisions

- Status: accepted
- Date: 2026-08-04

## Decision

Promotion is a second human-attested transition after candidate validation. It
creates an immutable, parent-linked memory revision in a local append-only
promotion ledger. The initial memory ID is deterministic from the reviewed
redacted-text hash and confirmed scope, so it does not expose a hash derived
from removed plaintext. Supersession must retain the locally verified semantic
identity and scope. Supersession and revocation require the exact current parent
revision; silent last-write-wins is not allowed.

An active revision is derived from the exact verified candidate text. The only
permitted transformation is deterministic redaction of sensitive findings.
Each redaction binds an exact UTF-8 byte range, a source hash, and an allowlisted
replacement. A redaction must equal the union of the findings it covers, so it
cannot be used as an unreviewed semantic rewrite. The caller also supplies the
expected final text hash.

Credential formats, private keys, credential assignments, credential-bearing
URIs, user-local paths, personal identifiers, opaque identifiers, and
high-entropy strings are scanned. Reports contain categories, byte ranges, and
hashes, never matched plaintext. Any uncovered or residual finding blocks
promotion.

Every promoted revision records `rule_change_authorization: not_granted`.
Authorization to modify `AGENTS.md`, a Skill, Hook, Plugin, or global rule is a
separate append-only decision bound to one exact memory revision, one exact
surface, and one exact logical target. Neither ledger modifies a rule surface
itself.

## Consequences

- `validated` and `promoted` remain distinct trust states.
- A later review rejection or quarantine makes an active revision ineligible
  for portability until it is superseded or revoked.
- A candidate generation that no longer covers the current evidence-ledger
  prefix cannot create a new promotion. Existing revisions remain bound to
  their verified historical evidence and explicit review state; ordinary later
  evidence and operational receipts do not invalidate them retroactively.
- Newly derived semantic conflicts are quarantined for explicit resolution
  instead of silently replacing an active revision.
- Superseded and revoked revisions remain auditable.
- Promotion metadata and final text must themselves pass the deterministic
  sensitive-content scan.
- The local promotion ledger is not a Git repository. Export to the separate
  private memory repository remains a later, independently validated step.
- Candidate, review, scan, and redaction hashes remain local proof material.
  A later Git exporter must create a separate allowlisted projection rather
  than copy a promotion record or revision object.
- A rule approval stops being effective when its exact memory revision is no
  longer the current eligible revision.
