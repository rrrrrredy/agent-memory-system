# Changelog

All notable changes will be documented in this file. The project follows
Semantic Versioning after the first tagged public release.

## [0.1.0] - 2026-08-11

### Added

- Loss-aware Codex, Claude Code, and OpenCode evidence adapters.
- Content-addressed blobs and an append-only, writer-locked evidence ledger.
- Episode reconstruction with compaction continuity checks.
- Evidence-backed candidate extraction, deduplication, and conflict quarantine.
- Caller-attested review, promotion, supersession, revocation, and rule-change
  approval with an explicit unauthenticated-identity boundary.
- Readable portable memory with explicit private Git synchronization.
- Deterministic scoped retrieval through CLI, MCP, and optional Agent bridges.
- Age-encrypted evidence backup and no-overwrite restore.
- Frozen-corpus continuous-learning evaluation and replay verification.
- Runtime compatibility reports and a verified candidate review queue.
- GitHub-hosted OpenCode plugin-to-ledger runtime smoke test.
- Reproducible synthetic quickstart for review, promotion, portable export, and
  retrieval on PowerShell and POSIX shells.
- Public JSON Schemas for every versioned quickstart result envelope.
- Separate history-import and executable-runtime compatibility status.
- Read-only manual release-build workflow, source/install/uninstall guidance,
  dependency updates, code ownership, and a pull-request checklist.
- A required public-tree privacy gate, exact frozen Quickstart manifest, and
  release provenance bound to protected `main` and successful checks for the
  exact release commit.
- Release receipts that constrain the canonical CI set and exact hosted
  platform/archive pairings.
- Installers that reject archives whose embedded binary version does not match
  the requested release, and successful exit codes for command help paths.

### Security

- Raw evidence is rejected from Git-facing storage boundaries.
- Portable memory is scanned for credential formats, private keys, local paths,
  personal identifiers, opaque identifiers, and high-entropy strings.
- Semantic conflicts, revision forks, altered history, and unexpected files
  fail closed.
- Rule changes require a separate revision-, surface-, and target-bound caller
  attestation plus the operator policy's explicit user approval.
- Promotion holds the evidence writer lock from freshness validation through the
  durable promotion append, closing the cross-ledger race window.
- Doctor reports an existing evidence writer lock as not ready and requires an
  explicit stale-lock recovery decision.
- The hosted OpenCode smoke binds a new `session.created` event to the server
  response and publishes only event and identifier hashes.
- GitHub Actions dependencies are pinned to full commit hashes.

### Known limits

- Capture cannot recover provider-hidden reasoning or data a runtime does not
  expose locally.
- Native Agent formats and hooks are version-sensitive.
- The current efficacy evaluator refuses a positive certification when native
  task execution is not independently verified.
- Physical macOS device acceptance is not yet claimed; macOS coverage uses
  GitHub-hosted runners.
- The maintainer-reported private Codex acceptance cannot be independently
  reproduced from this repository because neither the private transcript nor a
  correlatable source hash is published.
