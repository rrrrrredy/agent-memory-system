# ADR 0012: Relocation-safe source recovery

- Status: accepted
- Date: 2026-08-05

## Context

Agent runtimes and archival jobs may move historical transcripts after another
local artifact has recorded their original paths. Hashing only the current path
would create a new logical source, break coverage comparisons, and hide whether
an expected transcript was recovered or remained unavailable.

## Decision

Use a local, content-addressed recovery manifest that separates two identities:

- `logical_source_path_sha256` is the original source identity used by coverage,
  normalized events, and deterministic event IDs;
- `acquisition_path_hash` identifies the path from which the bytes were actually
  recovered.

An available entry also binds the expected content digest, byte count, and
thread ID. Every entry is resolved beneath one non-Git source root without
following symbolic links. All entries for one JSONL adapter are imported after
one verified ledger scan. OpenCode exports use the same validation contract.

An unavailable entry appends a deterministic missing gap. A summary, card, or
episode cannot be substituted for missing raw bytes. The exact manifest is
stored as local evidence, and repeated application is idempotent.

## Local indexing

This recovery path does not add SQLite. A one-time recovery benefits more from a
single verified batch scan than from a new database dependency. A later
rebuildable checkpoint or SQLite index may accelerate routine reconciliation,
but it cannot replace the append-only ledger, its hash chain, or full `doctor`
verification.

## Consequences

- Frozen evaluation corpora remain immutable; a new corpus can measure the
  additional raw evidence and the explicitly accounted missing sources.
- Recovery manifests and raw sources remain local-only and outside Git.
- Recovery can prove that exact bytes were found or not found; it cannot
  reconstruct provider-private or deleted content.
