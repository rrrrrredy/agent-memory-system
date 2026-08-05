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
- retrieval receipts link memory use to downstream outcomes.

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
- trusted scopes supplied by local configuration, never prompt content;
- complete portable-repository verification with fail-closed reads;
- compaction continuity tests;
- no-memory versus memory outcome evaluation.

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
