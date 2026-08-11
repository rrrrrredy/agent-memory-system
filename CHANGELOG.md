# Changelog

All notable changes will be documented in this file. The project follows
Semantic Versioning after the first tagged public release.

## Unreleased

### Added

- Loss-aware Codex, Claude Code, and OpenCode evidence adapters.
- Content-addressed blobs and an append-only, writer-locked evidence ledger.
- Episode reconstruction with compaction continuity checks.
- Evidence-backed candidate extraction, deduplication, and conflict quarantine.
- Human review, promotion, supersession, revocation, and rule-change approval.
- Readable portable memory with explicit private Git synchronization.
- Deterministic scoped retrieval through CLI, MCP, and optional Agent bridges.
- Age-encrypted evidence backup and no-overwrite restore.
- Frozen-corpus continuous-learning evaluation and replay verification.
- Runtime compatibility reports and a verified candidate review queue.
- GitHub-hosted OpenCode plugin-to-ledger runtime smoke test.

### Security

- Raw evidence is rejected from Git-facing storage boundaries.
- Portable memory is scanned for credential formats, private keys, local paths,
  personal identifiers, opaque identifiers, and high-entropy strings.
- Semantic conflicts, revision forks, altered history, and unexpected files
  fail closed.
- Rule changes require a separate revision-, surface-, and target-bound human
  authorization.

### Known limits

- Capture cannot recover provider-hidden reasoning or data a runtime does not
  expose locally.
- Native Agent formats and hooks are version-sensitive.
- The current efficacy evaluator refuses a positive certification when native
  task execution is not independently verified.
- Physical macOS device acceptance is not yet claimed; macOS coverage uses
  GitHub-hosted runners.
