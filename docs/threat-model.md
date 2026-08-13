# Threat model

Status: baseline for implementation and review, 2026-08-04.

## Assets

- Exact local task evidence and its integrity history.
- Credentials, personal information, source code, and tool outputs contained in
  that evidence.
- Promoted memories that may influence future Agent behavior.
- Device identities, synchronization credentials, encryption keys, and backup
  recovery material.
- Evaluation results used to decide whether a memory policy is safe.
- Portable loadouts, native execution receipts, and prospective study records
  used to repeat or assess memory delivery.

## Trust boundaries

1. Agent-native files and streams enter an adapter.
2. Adapter output enters the local evidence ledger.
3. Evidence is transformed into episodes and untrusted candidates.
4. A review decision promotes a redacted memory.
5. Promoted memory crosses devices through a private Git repository.
6. Retrieval output enters an Agent's prompt or tool context.
7. Optional encrypted evidence leaves the device for a separate backup backend.
8. An explicitly selected blind assessment payload may cross the network to the
   fixed OpenAI Responses endpoint after an exact payload-ID confirmation.
9. An exact local Codex process consumes a canonical request and may contact a
   remote provider outside this product's independent authority.
10. A read-only operational summary is served on a local loopback socket.

Private Git hosting is an access-control layer, not permission to upload raw
evidence. A local artifact marked `local_only` describes its canonical storage;
it does not mean that explicitly disclosed request content was never received
or retained by an API provider.

## Threats and required controls

### Silent capture loss

An adapter may miss rotated files, unsupported event types, truncated streams,
permissions, or compaction boundaries.

Controls:

- preserve exact source chunks before parsing;
- use byte cursors and source hashes;
- reconcile historical sources after restart;
- append explicit partial, missing, unknown, and gap records;
- measure capture coverage against source manifests.

### Evidence tampering or corruption

Local malware, disk errors, concurrent writers, or implementation defects may
change or partially append evidence.

Controls:

- content-addressed blobs and record hash chains;
- evidence roots rejected inside Git worktrees, including link-aliased paths;
- durable append followed by sync;
- exclusive cross-process evidence-writer locking;
- verification on open, backup, restore, and evaluation;
- append-only recovery events instead of rewriting verified history.

The current foundation has hashing, verification, an evidence-writer lock, and
scoped operation locks. A stale evidence lock is never guessed away; recovery
requires `doctor --clear-stale-writer-lock` after the operator verifies no writer
is active.
Automatic Git synchronization is a finite scheduled process protected by those
locks, not a resident daemon. A daemon remains out of scope without a stronger
cross-process lifecycle and signed-checkpoint design.

Native execution stores and replays the exact local runner and Codex bytes,
request, argument vector, raw JSONL, output, usage, and terminal state. That
closes supported local artifact-substitution and self-reported-run paths. It
does not withstand a fully compromised operating system or independently
authenticate remote provider behavior.

### Secret exfiltration

Raw messages and tool outputs may contain credentials or sensitive data. Simple
regular expressions are insufficient.

Controls:

- canonical evidence and assessment artifacts remain classified `local_only`;
- only an explicitly selected blind payload may be disclosed remotely, after
  exact-ID confirmation and a fail-closed scan of every text-bearing field;
- promoted memory is generated into a separate staging area;
- deterministic high-entropy, credential-format, private-key, and path scans;
- allowlisted schemas and explicit review;
- pre-commit and pre-push blocking;
- synthetic public fixtures only;
- backup encryption keys stored separately from backup data.

The controlled OpenAI path uses a fixed endpoint, no environment proxy,
redirect, retry, tool, background mode, conversation state, or stream. It saves
the exact request before transmission and records complete, partial, failed, and
absent responses distinctly. `store:false` is not Zero Data Retention, and the
sensitive-content scan only detects known patterns. Provider retention,
provider-side compromise, and unrecognized sensitive text therefore remain
explicit residual risks.

### Poisoned or false memory

An Agent, tool output, imported repository, or prompt-injected document may
propose a false rule.

Controls:

- candidates are untrusted and never automatically injected;
- evidence provenance and independence are recorded;
- model self-confirmation does not count as independent evidence;
- conflicts and stale claims fail closed;
- promotion is auditable and revocable;
- retrieval and injection receipts bind exact delivery;
- non-unknown adoption outcomes require causally bound result evidence;
- replayable task-attempt receipts bind comparable baseline and memory runs.

Human attestor and adoption-reporter identities are self-declared. The built-in
`evidence-score/v1` oracle is blind to task, attempt, condition, event, source, and time
identity and runs inside the evaluator without an external executable. It still
does not prove that its payload-hash criteria or labels are correct. Native registry executables receive
condition-bearing input and can access mutable host state; they are diagnostic
only and cannot satisfy efficacy prerequisites.

A `component` evaluation still permits caller-selected cases and thresholds and
therefore proves only its bounded diagnostic. `continuous_learning` rejects
caller-selected policy. It reconstructs the independent capture inventory and
a deterministic sample derived from the fixed policy and frozen corpus content
hash. Plans must cover every selected artifact exactly once, preserve
its exact task bytes and Agent assignment, and commit both arms, their order,
criteria, execution configuration, SUT, and oracle before any result.
The execution supervisor runs the exact local adapter bytes with challenge-bound
input that omits condition and acceptance criteria, and records exact output;
manually observed results are ineligible. A
started arm cannot be retried, and order is rechecked from ledger position.
Treatment delivery is not reported as use. This does not prove a claimed remote
provider/model or native Agent emitted the output. The current arbitrary-adapter
bridge therefore leaves `verified_agent_execution` false and cannot produce a
continuous release claim. Human-authored criteria may still be weak or wrong.
Episode-generation audits are append-only and deletion of referenced retained
generations fails verification, but a local administrator could still run an
undisclosed algorithm or directly forge local files outside
the supported flow. The security claim therefore assumes an honest local
operator while making supported cherry-picking and post-hoc labeling paths
auditable and fail-closed.

Prospective studies seal the population, complete native request fields,
immutable Agent-message acceptance hashes, deterministic balanced assignment,
Agent, elapsed period, and exact loadout before execution. Only the first
post-plan attempt can be eligible, and its request must match the plan exactly.
A built-in evaluator derives a dedicated outcome-evidence event from the
immutable Agent-message blob; the caller cannot supply a label or arbitrary
tool result, and synthetic observations remain non-evaluable. Because task
selection and operator behavior are not independently blinded, a complete
report is a descriptive association rather than a causal claim.

The evaluator binds its executable, system prompt, tool registry, harness, and
adapter artifacts, then freezes episode generation, the corpus, the complete
promotion projection, oracle registry, and capture snapshot in local evidence
blobs. Compaction labels must be sealed before detector generation. A late
eligible record, changed artifact, selective portable subset, missing planned
arm, receipt, projection, Agent stratum, category, or attestation fails closed.
An ordinary task outside a sealed plan remains outside the trial population.
The report remains `measurement_only`; a passing population is not universal or
longitudinal efficacy.

### Cross-device semantic conflict

Text files may merge cleanly while two devices make incompatible semantic
changes.

Controls:

- stable memory IDs and immutable revisions;
- expected-parent revision on every transition;
- semantic conflict validation after textual merge;
- explicit conflict objects and quarantine;
- no silent last-write-wins.

### Retrieval overreach and goal drift

Irrelevant memory can consume context, override current user intent, or amplify
an earlier compaction error.

Controls:

- task, Agent, OS, device, repository, version, and time scopes;
- explicit token and item budgets;
- an independent UTF-8 byte budget;
- current user instruction outranks recalled memory;
- retrieval, exact delivery, adoption, and outcome receipts;
- complete causal task-attempt windows with exact result and user-message
  coverage;
- trusted scopes supplied by local configuration, never prompt content;
- complete portable-repository verification with fail-closed reads;
- compaction continuity tests;
- no-memory versus memory outcome evaluation.
- immutable loadouts that bind exact active revision heads, approved Agents,
  trusted scope, and delivery budgets;
- complete composite loadout receipts with no stale or partial fallback;
- a loopback-only dashboard that serves summary metadata and rejects writes.

### Backup compromise or unrecoverability

Encrypted backup may be stolen, corrupted, or impossible to decrypt.

Controls:

- authenticated encryption and versioned manifests;
- keys never stored with backup objects;
- independent retention and credentials;
- scheduled integrity checks and restore drills;
- no use of the promoted-memory Git repository as evidence backup.

The integrated backup path uses native age hybrid keys, an encrypted manifest,
two-pass source hashing, streaming ciphertext and ledger verification, and a
no-overwrite staging restore. It refuses operation locks, links, Git-contained
destinations, undeclared entries, and path traversal. The private identity is
never written into an archive or sidecar.

## Out of scope

- Preventing a fully compromised local administrator from reading unlocked
  evidence.
- Recovering provider-private reasoning never exposed to the local runtime.
- Treating an LLM judgment as a cryptographic or factual proof.
- Authenticating a remote provider or model solely from a local Codex process.
- Preventing another process running as the same local user from reaching the
  loopback dashboard. The dashboard is not exposed remotely and is not an
  authentication boundary.
