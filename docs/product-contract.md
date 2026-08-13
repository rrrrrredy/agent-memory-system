# Product contract

Status: accepted baseline, 2026-08-04.

## Definition

Agent Memory System is a local-first, cross-agent, evidence-driven, evaluable
continuous memory system. It preserves each supported agent's locally available
task trajectory, reconstructs episodes, validates candidate experience, and
requires policy-based operator review and promotion. Those decisions are stored
as caller-supplied attestations bound to evidence; the implementation does not
authenticate that a reviewer or approver identity is human. Only redacted
promoted memory is synchronized through a separate private Git repository.

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
caller-supplied reviewer attestation, confirmed scope, and evidence basis in an
append-only record. The attestation is not identity authentication. Validation alone does not create promoted memory.

A review packet MAY reduce repeated operator lookup, but it MUST be immutable,
local-only, and bound to the exact candidate generation, evidence-ledger prefix,
candidate content and displayed-text hashes, provenance, and current review
metadata. Consuming a packet MUST re-verify those bindings. A packet MUST NOT
authenticate a person, combine validation with promotion, approve a conflict
group, or make an otherwise stale candidate current.

A promotion transition MUST bind the exact current validation record,
candidate content and semantic identity, confirmed scope, deterministic scan,
reviewed redacted-text hash, and expected parent revision. Redaction may remove
detected sensitive ranges but may not serve as an unreviewed semantic rewrite.
The candidate generation MUST cover the current verified evidence-ledger
prefix at promotion time. Later evidence MUST prevent any new promotion from
reusing that stale generation, but it MUST NOT retroactively erase an existing
attested revision. An active memory MUST fail closed for export and
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
- Portable loadouts are immutable content-addressed metadata in the private
  memory repository. They may reference only exact active portable revision
  heads and MUST become ineligible when any head changes.

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
- A loadout delivery MUST verify the complete portable repository, exact
  revision heads, Agent allowlist, trusted scope, and both budgets before
  rendering one context. It MUST record the exact composite content and
  underlying retrieval receipts locally.
- Loadout delivery is not adoption or outcome evidence. A stale loadout MUST
  fail as a whole and MUST NOT follow new revision heads automatically.

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

False-memory ground truth MUST bind the exact measured subject and label to a
prior local human attestation. Compaction ground truth MUST be one complete
human-reviewed pack sealed before episode generation or drift detection. It MUST
supply only expected labels; observed labels MUST come from the later,
hash-bound continuity detector. Repeated-correction and paired-outcome
measurements MUST derive from complete sealed pair plans and replayed supervised
task receipts. Every plan MUST bind both arms, execution order, frozen corpus,
task artifacts, SUT, and oracle before any result. Every arm MUST bind a complete
causal window, exact memory exposure, and one execution and oracle verdict
receipt. Their authority is measurement-only. The model under evaluation cannot
certify its own interpretation merely by supplying a label or hash. The trial
universe MUST be deterministically derived from frozen corpus artifacts, the
fixed policy, and corpus content hash without a caller-chosen seed; selected
artifacts, task bytes, Agent strata, and derived task/pair
identities MUST have complete one-to-one coverage before execution. A supervised
start MUST make its arm non-retryable, and the sealed arm order MUST be enforced
during execution and replay. Memory delivery MUST NOT be reported as adoption
without a separate evidence-bound observation.
Each controlled attempt MUST bind the exact system prompt, tool registry,
harness, and Agent adapter as local content-addressed blobs. Declared hashes
without those bytes are insufficient. The supervisor MUST run the exact local
adapter artifact with challenge-bound input that omits condition and acceptance
criteria, and record its exact result. Every start MUST produce one terminal
receipt; non-zero exits, timeouts, start failures, and empty output MUST be
canonical failures rather than retry opportunities. This does not authenticate
a remote provider, model, or native Agent runtime.

A metric with no denominator is `not_evaluable`, never a passing zero. A
component profile may exercise one bounded caller-configured quality check. The
`continuous_learning` profile MUST use `continuous-learning-policy/v1`, its
fixed thresholds, the independent capture-supervisor inventory, and the complete
set of sealed paired plans committed before the first result. It MUST also bind
the current system artifact, verified episode generation, frozen corpus, full
local promotion projection, local oracle registry, and historical capture
snapshot. A caller-selected portable subset is insufficient.
Those mutable dependencies MUST be frozen as local content-addressed blobs before
the run. Only the blind, hermetic built-in `evidence-score/v1` oracle may satisfy
efficacy prerequisites; native executable oracles remain diagnostic. An unknown
token count MUST remain not evaluated instead of becoming a measured zero.
Missing projections, categories, per-Agent strata,
receipts, attestations, stale inputs, or replay failures MUST be issues. A report
can be release-ready only for those exact bound populations when all gates pass.
It MUST also have verifiable native Agent execution provenance; an arbitrary
local adapter receipt is diagnostic and MUST NOT satisfy that prerequisite.
It MUST NOT be described as covering ordinary tasks that lacked an evaluation
contract, promoting memory, authorizing a rule change, or proving general
real-world efficacy.

### Native execution and prospective studies

A native execution receipt MAY establish which exact local Agent and runner
bytes were staged, which canonical request and arguments were used, which raw
events and output were observed, and how the local process terminated. Replay
MUST bind all those artifacts, event order, usage, outcome, and receipt
identity. It MUST preserve terminal failures and MUST NOT authenticate a remote
provider, server-side model, account, or provider-hidden reasoning.

A prospective study MUST append its complete task and cluster identities, every
native request field, a private content-addressed snapshot of each complete
working directory, immutable acceptance assertions, Agent, exact current
loadout, minimum elapsed period, deterministic assignment policy, and all
assignments before any eligible execution. Task IDs MUST be unique across the
evidence store. Baseline tasks MUST have no memory receipt. Memory tasks MUST
bind a verified context receipt for the exact embedded loadout.

The supported study runner MUST atomically append one single-use reservation
before starting the local process. That reservation MUST bind the plan, task,
condition, exact request, workspace snapshot, and loadout. A start failure,
timeout, nonzero exit, or missing result MUST append a terminal failure and
consume the reservation. A direct native receipt or a second reservation MUST
be ineligible. The runner MUST materialize the sealed workspace in a private
temporary directory and bind the native start, visible events, receipt, and
study terminal as causal descendants. Later changes to the source directory
MUST NOT affect the executed bytes.

Within the honest-local-operator boundary this enforces the first study-bound
attempt. It cannot prove that no undisclosed rehearsal occurred outside the
supported command and MUST NOT be described as enforcing the first execution
performed by a local administrator.

The outcome MUST be derived by the supported built-in evaluator from the
complete immutable Agent-message blob and prospectively sealed acceptance assertions.
A dedicated local outcome-evidence event MUST bind the plan, execution,
acceptance hash, assertion results, and derived outcome before the observation.
Replay MUST reproduce that event exactly. The caller MUST NOT submit an outcome
label or arbitrary tool result. Missing tasks, duplicate observations,
synthetic observations, insufficient elapsed time, fewer than two observations
per arm, changed requests, retries, or non-replayable outcomes MUST yield
`not_evaluable`.

Even a complete report is limited to a prospective descriptive association.
The supported protocol does not blind task selection, independently randomize
operator behavior, or attest the provider. It MUST NOT be described as causal
or independently provider-certified efficacy.

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
- Treating a local process receipt or descriptive study as independent proof of
  a provider, model, human identity, or causal learning effect.
- Measuring success by note count, hook invocations, or retrieval volume alone.
