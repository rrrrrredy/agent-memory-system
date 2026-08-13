# Changelog

All notable changes will be documented in this file. The project follows
Semantic Versioning after the first tagged public release.

## [0.3.0] - 2026-08-13

### Added

- Immutable, content-addressed portable memory loadouts with exact active
  revision references, Agent allowlists, trusted scope, delivery budgets,
  stale-head rejection, and composite local context receipts.
- Immutable candidate review packets that bind displayed text hashes,
  provenance, generation identity, the current evidence prefix, and review
  metadata while preserving separate validation and promotion decisions.
- A native Codex execution bridge that preserves exact local runner and Codex
  bytes, canonical requests, arguments, raw JSONL, usage, terminal output, and
  completed or failed receipts, plus complete replay verification.
- Prospective longitudinal study plans that seal complete native requests,
  content-addressed workspace snapshots, and immutable acceptance hashes;
  deterministically counterbalance assignments; reserve one study-bound
  attempt before execution; derive outcomes from complete replayed
  Agent-message bytes; and report descriptive results.
- A loopback-only read-only dashboard for evidence, review, promotion,
  retrieval, loadout, native-execution, and study verification summaries.
- Public JSON Schemas and real producer instances for every new v0.3 protocol.
- A static GitHub Pages product site and a Pages deployment workflow.

### Security

- Loadouts become unusable when any referenced revision is superseded,
  revoked, missing, or no longer the exact active head; no partial or silent
  replacement is allowed.
- Loadout-backed native execution requires an exact verified context receipt
  and holds the portable repository use lock through the terminal receipt.
- Native execution stages and re-hashes content-addressed executable bytes,
  records terminal failures without a retry loophole, and labels remote
  provider, model, and private-reasoning authority as unverified.
- Longitudinal execution seals workspace bytes, writes a single-use reservation
  before Codex starts, records every terminal failure, and rejects generic or
  replacement receipts. Callers cannot submit outcome labels, and synthetic
  observations cannot clear the evaluability gate.
- The dashboard refuses non-loopback listeners, serves summary metadata only,
  exposes no write route, ships no external browser resources, and replaces
  raw verifier errors with stable issue codes so private paths are not returned.

### Known limits

- Native process receipts prove the supported local execution graph, not the
  identity or behavior of a remote provider or server-side model.
- Claude Code remains protocol-conformant without an equivalent native
  execution receipt. OpenCode runtime coverage remains hosted and provider-free.
- No completed real-world longitudinal study is claimed in this release;
  eligible reports are descriptive associations, not causal certification.

## [0.2.0] - 2026-08-12

### Added

- `onboard codex` imports, derives, verifies, and reports the next operator
  action in one command without approving or promoting memory.
- `status` summarizes evidence integrity, current review state, promoted local
  revisions, portable verification, and the next safe action.
- Review and promotion commands can resolve the latest verified candidate
  generation, removing generated-path plumbing from the ordinary workflow.
- A native Codex paired diagnostic seals its complete plan before execution,
  deterministically assigns arm order from bound task artifacts, records raw JSONL and token
  usage, and runs exact staged Codex and local oracle bytes.
- Public schemas and real producer tests for onboarding, status, review summary,
  benchmark plans, sealed plans, reports, local verification, and aggregate
  public receipts.
- Episode derivation v1alpha2 preserves dotted project tokens without reusing v1alpha1 generations.

### Security

- Benchmark input plans, runner/Codex/oracle executables, workspaces, oracle
  overlays, raw events, agent messages, and outputs are retained as local content-addressed evidence.
- Oracle arguments cannot reference unsealed files, and the reserved result
  directory is rejected from input artifacts.
- Caller-provided context is permanently diagnostic. Only promoted memory
  delivered through a replay-verified retrieval/injection receipt can count as
  product memory exposure.
- Observed benefit requires 20 verified-retrieval pairs from 20 distinct task
  clusters, sealed no-tools policy, zero tool calls, no execution issue,
  positive paired outcomes, and a one-sided sign-test value at or below 0.05.
  It remains bounded to the exact sealed suite.

### Evidence

- The published synthetic aggregate receipt records 20 pairs and clusters,
  baseline 1/20, memory 20/20, 19 wins, 1 tie, 0 losses, 40 zero-tool arms,
  and a one-sided sign-test value of 0.0000019073486328125. The local verifier
  replayed 204 records and 229 blobs without issues; raw evidence remains local.

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
  the requested release, plus group-specific output and successful exit codes for command help paths.

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
