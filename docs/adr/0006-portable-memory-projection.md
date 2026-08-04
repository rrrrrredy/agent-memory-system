# ADR 0006: Immutable portable memory projection

- Status: accepted
- Date: 2026-08-04

## Decision

The private memory repository stores one canonical Markdown file per immutable
portable revision. It does not store a mutable current-state file, SQLite
database, local promotion record, or generated index as canonical data. Current
state is derived from parent links across revision files.

The portable projection uses the stable promoted memory ID but derives a new
portable revision ID from an allowlisted representation. The representation
contains only:

- action, status, kind, and parent revision;
- confirmed scope;
- reviewed redacted text and its content hash;
- coarse evidence-basis categories;
- the rule-change protection bit; and
- a `private_git` classification.

Candidate, review, scanner, finding, and redaction hashes and ranges remain
local. Device identifiers, approver identifiers, reasons, timestamps, source
paths, and raw evidence are also excluded. A portable revision always records
`rule_change_authorization: not_granted`; rule changes still require a separate
current user decision on the consuming device.

The repository is strict and append-only. Files have deterministic paths and
canonical LF encoding. An existing revision path may only contain identical
bytes. Unknown files, changed repository metadata, sensitive content, invalid
parents, multiple roots, divergent children, duplicate semantic memories, and
opposing semantic memories fail verification.

## Consequences

- Git normally merges independent immutable revision files without line-level
  conflicts.
- Two children of the same parent remain visible as a fork and are quarantined;
  no last-write-wins rule selects a child.
- Equivalent independent revisions converge only when every portable field is
  identical; otherwise the difference remains explicit.
- Rebuildable indexes and current-state views stay outside Git.
- Export requires the current active local revision to remain eligible. A
  revoked local chain may export its tombstone.
- The private repository can be inspected as readable Markdown without
  exposing local evidence proofs.
