# Product contract

Status: accepted baseline, 2026-08-04.

## Definition

Agent Memory System is a local-first, cross-agent, evidence-driven, evaluable
continuous memory system. It preserves each supported agent's locally available
task trajectory, reconstructs episodes, validates candidate experience, and
requires policy-based human promotion. Only redacted promoted memory is
synchronized through a separate private Git repository.

Continuous learning means that recalled memory measurably reduces repeated
mistakes and repeated work without increasing false guidance, goal drift, or
context cost beyond an explicit budget. Accumulating notes alone is not
continuous learning.

## Complete-process-record requirement

For Codex, Claude Code, and OpenCode, an adapter MUST preserve every artifact
available to it, including:

- every user instruction, correction, attachment reference, and feedback item;
- every model message, commentary update, plan, and final response;
- every tool call, argument, output, error, timing record, and correlation ID;
- every approval, denial, file-change record, sub-agent event, and system event;
- each compaction trigger, the source history available before compaction, the
  compacted representation, and the replacement history after compaction;
- every reasoning artifact the runtime exposes or persists, including plaintext
  reasoning, reasoning summaries, and opaque encrypted payloads;
- source offsets, hashes, adapter version, device identity, timestamps, and
  capture-completeness metadata.

The system MUST distinguish:

- `raw_exposed`: exact reasoning text was available and preserved;
- `summary_only`: only a provider-generated reasoning summary was available;
- `encrypted_opaque`: encrypted bytes were available but plaintext was not;
- `not_exposed`: the provider did not make the reasoning artifact available;
- `not_applicable`: the event is not a reasoning event.

No implementation may describe `summary_only`, `encrypted_opaque`, or
`not_exposed` data as complete plaintext reasoning. Local model computation
does not by itself prove local persistence. Content that never reaches local
storage or an exposed stream cannot be recovered by this product.

If capture is partial, interrupted, unsupported, corrupt, or permission
limited, the adapter MUST append an explicit gap event. Silent loss is a
contract failure.

## Truth and derivation

1. Exact source bytes and append-only evidence records are local truth.
2. A normalized timeline and episode are reproducible derivations.
3. Candidate experience is an untrusted hypothesis.
4. A validated candidate has supporting evidence but is not automatically
   active.
5. Promoted memory is the only class eligible for automatic retrieval,
   injection, and Git synchronization.
6. Superseded and revoked memory remains auditable and is never silently erased.
7. SQLite, vector stores, caches, and embeddings are disposable derivations.

Raw evidence may be searched deliberately for investigation. Candidate
experience may be queried in review workflows. Neither is automatically
injected into agent context.

A `review_ready` candidate is still untrusted review material. It is not a
validated candidate or promoted memory, and it is never automatically eligible
for promotion.

A review transition MUST bind the candidate content hash, expected prior state,
human reviewer attestation, confirmed scope, and evidence basis in an
append-only record. Validation alone does not create promoted memory.

Compaction continuity analysis MUST distinguish confirmed correction evidence
from lexical omission risk and unavailable compacted representations. A missing
phrase alone is not proof that the task goal drifted.

## Promotion policy

- An explicit user `remember` instruction may be promoted after deterministic
  secret scanning and scope confirmation.
- An explicit user correction is a high-priority candidate and may use a
  streamlined confirmation path when the corrected fact is unambiguous.
- Model-inferred experience requires outcome evidence, stable repeated evidence,
  or explicit user confirmation.
- The same model proposing and then approving its own claim is not independent
  evidence.
- Contradictions, stale facts, ambiguous corrections, and semantic conflicts are
  quarantined for review.
- Updating `AGENTS.md`, Skills, hooks, plugins, or global rules always requires
  explicit user approval, even when the underlying memory is promoted.

## Storage and synchronization

- Raw evidence is local-only by default.
- Optional raw-evidence disaster recovery uses encrypted snapshots and a backend
  fully separate from the readable memory Git repository.
- The public tool repository and private personal-memory repository are
  separate.
- The private memory repository contains only reviewed, redacted promoted
  memory in Git-auditable Markdown/YAML or append-only events.
- Complete transcripts, raw tool outputs, reasoning artifacts, sensitive data,
  agent state databases, and internal session directories MUST NOT enter Git.
- Manual synchronization is the default. Automatic synchronization is optional,
  observable, reversible, and recoverable.
- Semantic conflicts are reported explicitly. Silent last-write-wins is
  forbidden.
- Deletions use auditable tombstones or revocations, not unexplained history
  removal.

## Evaluation contract

### Continuous-learning quality

- capture coverage;
- false-memory rate;
- repeated user-correction rate;
- goal or constraint drift across compaction;
- retrieval token cost;
- downstream task-result change with and without retrieved memory;
- provenance and adoption receipts for each retrieval.

### Cross-device and cross-agent reliability

- Windows/macOS bidirectional synchronization;
- Codex, Claude Code, and OpenCode use the same promoted memory;
- offline operation from a local clone;
- manual synchronization reliability;
- automatic synchronization enable, disable, retry, and recovery;
- secret, duplicate, failure, and semantic-conflict detection;
- verifiable new-device restore and load.

The legacy context-journal cards, index, and rollouts are frozen evaluation
corpora. They are not bulk-promoted into active memory.

## Explicit non-goals

- Recovering private chain-of-thought that a provider never exposes.
- Uploading all threads because a repository is private.
- Mirroring `~/.codex`, agent session folders, state databases, or native
  memory repositories.
- Treating a hook, Skill, plugin, SQLite database, or vector index as the sole
  source of truth.
- Automatically turning model-written summaries into global rules.
- Measuring success by note count, hook invocations, or retrieval volume alone.
