# Delivery roadmap

This sequence preserves evidence before adding automation that can influence
future tasks.

## M0: Contract and ledger foundation

Status: implemented and covered by cross-platform integrity tests.

- Product contract, threat model, and architecture decisions.
- Versioned evidence-event schema.
- Content-addressed raw blobs.
- Append-only record hash chain and integrity doctor.
- Synthetic tests for exact content, opaque reasoning, missing reasoning, and
  tampering.

Exit: the repository can demonstrate that locally available bytes are retained
without calling them promoted memory.

## M1: Three complete Agent adapters

Status: implemented; runtime formats remain version-sensitive.

- Codex history importer and incremental reconciler.
- Claude Code history importer and incremental reconciler.
- OpenCode history importer and incremental reconciler.
- Adapter capability manifests and explicit unsupported-field gaps.
- Crash, rotation, duplicate, partial-write, and compaction fixtures.

Implementation may proceed as Codex, then Claude Code, then OpenCode, but M1
does not exit until all three pass the same contract.

## M2: Episodes and evidence-driven learning

Status: implemented with evidence-bound validation and promotion attestations.
Deployment policy requires operator review; caller identity is not
authenticated by the CLI.

- Timeline and episode reconstruction.
- Compaction continuity and goal/constraint drift checks.
- Candidate extraction with traceable evidence spans.
- Deduplication, conflict quarantine, review, promotion, supersession, and
  revocation.
- Protection against self-confirming model evidence.
- Local capture supervision with content-hashed source inventory, explicit
  missing-source evidence, foreground watch, and opt-in freshness gates.

## M3: Portable memory and Git synchronization

Status: implemented; manual synchronization remains the default.

- Readable Markdown/YAML memory protocol with immutable revisions.
- Deterministic secret scanning.
- Separate private data-repository bootstrap.
- Manual pull/validate/merge/push workflow.
- Optional automatic synchronization using the same validation path.
- Offline edits, duplicates, tombstones, textual conflicts, and semantic
  conflicts.

No raw evidence is migrated when the private repository is created.

## M4: Retrieval, evaluation, and recovery

Status: retrieval, evaluation controls, and recovery are implemented. Core
runtime compatibility includes real Codex evidence and a hosted OpenCode plugin
smoke. Independent longitudinal efficacy evidence remains an
operational acceptance requirement, not a claim made by the synthetic suite.

- Cross-agent local query and MCP retrieval.
- Bounded injection with provenance and adoption receipts.
- Continuous-learning regression suite using frozen legacy cards and rollouts.
- Evidence-bound case attestations and non-vacuous quality gates.
- Windows/macOS and three-Agent synchronization suite.
- Optional encrypted evidence backup and verified restore.
- Install, doctor, upgrade, and new-device recovery flows.

## M5: Repeatable operations and prospective evidence

Status: operational controls are implemented in v0.3.0. A completed real-world
longitudinal study remains future evidence, not a release claim.

- Immutable exact-revision memory loadouts with scope, Agent, and budgets.
- Immutable evidence-bound review packets for lower-friction operator review.
- Replay-verifiable local Codex execution receipts with explicit provider
  authority limits.
- Honest Claude Code protocol-only and hosted-only OpenCode execution evidence.
- Prospective full-task contracts, deterministic balanced assignment,
  first-attempt enforcement, built-in replay-derived outcome evidence, and
  descriptive longitudinal reports.
- Loopback-only read-only operational dashboard.
- Static public product documentation through GitHub Pages.

Exit for implementation: public schemas, producer instances, adversarial
failure tests, cross-platform CI, one real local Codex acceptance, and release
artifact verification. Exit for longitudinal efficacy is separate: the full
prospective period and population must complete without weakening the claim
boundary.
