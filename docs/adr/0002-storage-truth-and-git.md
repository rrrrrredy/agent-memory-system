# ADR 0002: Three storage zones and two repositories

- Status: accepted
- Date: 2026-08-04

## Decision

Maintain three physically and logically separate storage zones:

1. **Local evidence:** exact captured bytes, normalized append-only events,
   ingestion checkpoints, and derived local indexes.
2. **Promoted memory:** redacted, human-approved, scoped, versioned memory in a
   private Git repository.
3. **Encrypted evidence backup:** optional snapshots stored outside the promoted
   memory repository.

The public code repository contains implementation, schemas, synthetic fixtures,
and evaluation harnesses only.

## Integrity model

- Raw blobs are content-addressed with SHA-256.
- Normalized records form an append-only hash chain.
- Each import stores source identity, byte offsets or cursors, adapter version,
  and a completeness result.
- Interrupted final records are detected and repaired only by appending a
  recovery record; verified history is not rewritten.
- Derived indexes can be deleted and rebuilt from evidence and promoted memory.

## Git model

- Promoted memory uses stable IDs and immutable revisions.
- Changes carry expected prior revisions; divergent semantic edits become
  explicit conflicts.
- Revocation and supersession are append-only state transitions.
- A deterministic secret scan blocks commits on uncertainty.
- Manual pull, validate, merge, and push is the default workflow.
- Automatic sync uses the same validation path and can be disabled without
  affecting local retrieval.

## Consequences

A private repository is not treated as a security boundary for raw evidence.
Recovery of evidence and portability of promoted memory are independent
features with independent credentials, retention, and restore tests.
