# Delivery roadmap

This sequence preserves evidence before adding automation that can influence
future tasks.

## M0: Contract and ledger foundation

- Product contract, threat model, and architecture decisions.
- Versioned evidence-event schema.
- Content-addressed raw blobs.
- Append-only record hash chain and integrity doctor.
- Synthetic tests for exact content, opaque reasoning, missing reasoning, and
  tampering.

Exit: the repository can demonstrate that locally available bytes are retained
without calling them promoted memory.

## M1: Three complete Agent adapters

- Codex history importer and incremental reconciler.
- Claude Code history importer and incremental reconciler.
- OpenCode history importer and incremental reconciler.
- Adapter capability manifests and explicit unsupported-field gaps.
- Crash, rotation, duplicate, partial-write, and compaction fixtures.

Implementation may proceed as Codex, then Claude Code, then OpenCode, but M1
does not exit until all three pass the same contract.

## M2: Episodes and evidence-driven learning

- Timeline and episode reconstruction.
- Compaction continuity and goal/constraint drift checks.
- Candidate extraction with traceable evidence spans.
- Deduplication, conflict quarantine, review, promotion, supersession, and
  revocation.
- Protection against self-confirming model evidence.

## M3: Portable memory and Git synchronization

- Readable Markdown/YAML memory protocol with immutable revisions.
- Deterministic secret scanning.
- Separate private data-repository bootstrap.
- Manual pull/validate/merge/push workflow.
- Optional automatic synchronization using the same validation path.
- Offline edits, duplicates, tombstones, textual conflicts, and semantic
  conflicts.

No raw evidence is migrated when the private repository is created.

## M4: Retrieval, evaluation, and recovery

Status: implemented in the v1alpha1 foundation.

- Cross-agent local query and MCP retrieval.
- Bounded injection with provenance and adoption receipts.
- Continuous-learning regression suite using frozen legacy cards and rollouts.
- Evidence-bound case attestations and non-vacuous quality gates.
- Windows/macOS and three-Agent synchronization suite.
- Optional encrypted evidence backup and verified restore.
- Install, doctor, upgrade, and new-device recovery flows.
