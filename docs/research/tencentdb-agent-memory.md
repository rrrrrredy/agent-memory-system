# TencentDB-Agent-Memory: adopt, modify, reject

Reviewed against TencentCloud/TencentDB-Agent-Memory v2.0.0 on 2026-08-04.
The project is a useful architecture reference and negative-regression source,
but is not the code base for Agent Memory System.

## Adopt

- Separate the shared memory core from thin Agent adapters.
- Keep readable Markdown as an upper-layer asset and SQLite/vector search as a
  rebuildable derivative.
- Use immutable revisions and optimistic concurrency for memory changes.
- Apply progressive disclosure and explicit retrieval budgets.
- Turn correction scenarios such as issue 706 into regression cases.

## Modify

- Replace the project's L0/L1/L2/L3 flow with:
  `raw evidence -> episode -> candidate -> validated -> promoted ->
  superseded/revoked`.
- Candidate retrieval may use lexical or vector search, but deduplication and
  conflict checks fail closed. A model or parser failure preserves an unresolved
  candidate; it never promotes or stores it as active memory by default.
- Extend operational metrics with capture coverage, false-memory rate, repeated
  corrections, compaction drift, retrieval cost, adoption, and task outcomes.
- Implement first-party Codex, Claude Code, and OpenCode adapters against the
  shared protocol.

## Reject

- Treating filtered user/assistant text as a complete evidence ledger.
- Deleting raw evidence through ordinary memory-retention cleanup.
- Silent last-write-wins profile synchronization.
- Treating code-repository refresh or session-cache refresh as cross-device
  memory synchronization.
- Adopting the full multi-service deployment as a runtime dependency.

Primary references:

- <https://github.com/TencentCloud/TencentDB-Agent-Memory>
- <https://github.com/TencentCloud/TencentDB-Agent-Memory/issues/706>
- <https://github.com/TencentCloud/TencentDB-Agent-Memory/issues/82>
