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

In `evidence-event/v1alpha1`, a non-reasoning `kind` is the canonical
`not_applicable` representation and the optional `reasoning` object is omitted.
For a `reasoning` event, the object is mandatory and may never use
`not_applicable`. Consumers must not interpret an omitted object on another
event kind as `not_exposed` reasoning.

No implementation may describe `summary_only`, `encrypted_opaque`, or
`not_exposed` data as complete plaintext reasoning. Local model computation
does not by itself prove local persistence. Content that never reaches local
storage or an exposed stream cannot be recovered by this product.

If capture is partial, interrupted, unsupported, corrupt, or permission
limited, the adapter MUST append an explicit gap event. Silent loss is a
contract failure.

If an available raw source moved after its logical path was recorded, recovery
MUST retain the original source identity, separately record the acquisition
path identity, and verify the recovered bytes and thread identity before
appending normalized events. A derived summary or card may never stand in for
missing raw source bytes. An exhausted local search is recorded as a stable
missing gap, not silently removed from coverage.

Capture evaluation MUST report raw-byte coverage separately from accounted
missing sources. An explicit missing gap improves loss accounting, not raw
capture, and cannot satisfy a raw capture coverage gate.

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

A promotion transition MUST bind the exact current validation record,
candidate content and semantic identity, confirmed scope, deterministic scan,
reviewed redacted-text hash, and expected parent revision. Redaction may remove
detected sensitive ranges but may not serve as an unreviewed semantic rewrite.
The candidate generation MUST cover the current verified evidence-ledger
prefix at promotion time. Later evidence MUST prevent any new promotion from
reusing that stale generation, but it MUST NOT retroactively erase an existing
human-approved revision. An active memory MUST fail closed for export and
retrieval if its bound source validation later ceases to be current, its proof
fails verification, or the revision is superseded or revoked. Newly derived
semantic conflicts remain quarantined until explicit review resolves them.

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
- Rule-change approval MUST be a separate append-only event bound to one exact
  promoted revision, one exact rule surface, and one exact logical target.
  Promotion itself never grants that authorization.

## Storage and synchronization

- Raw evidence is local-only by default.
- The evidence ledger refuses any root inside a Git worktree, including paths
  that enter one through a directory link.
- Optional raw-evidence disaster recovery uses encrypted snapshots and a backend
  fully separate from the readable memory Git repository.
- Backup decryption identities are never stored with backup objects. Creation
  and verification bind authenticated ciphertext to a versioned file manifest,
  the evidence chain, and referenced blobs.
- Restore is no-overwrite and must pass complete verification in staging before
  a new evidence directory is committed. A restored logical store is a disaster
  replacement, not an active-active replica.
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

## Retrieval contract

- Ordinary task retrieval reads only active promoted revisions from the
  verified portable memory repository.
- Raw evidence and candidates remain available through separate local forensic
  and review workflows, never automatic injection.
- Any repository-integrity or semantic-conflict issue blocks the complete read.
- Agent, repository, project, and task scopes are exact and come from trusted
  local configuration rather than model-provided text.
- Item, estimated-token, and UTF-8 byte limits apply to every delivered block.
- Retrieval, delivery, adoption, and outcome are recorded as distinct local
  observations.
- Hooks and plugins are optional; the common local MCP server is the shared
  cross-Agent query interface.
- Agent availability errors fail open without memory. Memory verification fails
  closed without partial or stale fallback.

## Evaluation contract

### Continuous-learning quality

- capture coverage;
- false-memory rate;
- repeated user-correction rate;
- goal or constraint drift across compaction;
- retrieval token cost;
- downstream task-result change with and without retrieved memory;
- provenance and adoption receipts for each retrieval.

Raw-byte capture coverage and normalized-event projection quality MUST be
reported separately. A parser gap does not prove loss of preserved source
bytes, and a parsed record does not prove full source capture.

False-memory and compaction-drift measurements MUST bind the exact measured
value to a prior local append-only attestation, and their ground-truth labels
require a human attestor. Repeated-correction and paired-outcome measurements
MUST instead be derived from replayed local task-attempt receipts. Those
receipts bind a complete causal evidence window, task and acceptance-contract
hashes, exact memory exposure, and an oracle verdict; their authority is
measurement-only. The model under evaluation cannot certify its own
interpretation merely by supplying a label or hash in the evaluation input.

A metric with no denominator is `not_evaluable`, never a passing zero. A
component profile may exercise one bounded, measurement-only quality check. The
`continuous_learning` profile requires all six quality categories, all twelve
caller-supplied gates, positive minimum counts for correction opportunities and
paired outcomes, and successful resolution and replay of every required
artifact and receipt. It remains measurement-only and cannot establish product
efficacy or release readiness until fixed versioned policy, deterministic
complete-population reconstruction, and independently runnable oracle checks
are implemented together.

### Cross-device and cross-agent reliability

- Windows/macOS bidirectional synchronization;
- Codex, Claude Code, and OpenCode use the same promoted memory;
- offline operation from a local clone;
- manual synchronization reliability;
- automatic synchronization enable, disable, retry, and recovery;
- secret, duplicate, failure, and semantic-conflict detection;
- verifiable new-device restore and load.

The legacy context-journal cards, index, and rollouts are frozen evaluation
corpora. They are not bulk-promoted into active memory. Local regression review
packs MUST bind the frozen corpus and current derivation identities, exclude
episodes outside that corpus, retain negative controls and conflicts, and
remain unlabeled until an independent review or attestation is recorded.

## Explicit non-goals

- Recovering private chain-of-thought that a provider never exposes.
- Uploading all threads because a repository is private.
- Mirroring `~/.codex`, agent session folders, state databases, or native
  memory repositories.
- Treating a hook, Skill, plugin, SQLite database, or vector index as the sole
  source of truth.
- Automatically turning model-written summaries into global rules.
- Measuring success by note count, hook invocations, or retrieval volume alone.
