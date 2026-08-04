# Claude Code adapter capability contract

The adapter treats Claude Code transcript JSONL as local evidence, not as a
stable provider API. It preserves every captured byte before parsing it.

## Captured now

- top-level transcript records, including unknown future record types;
- user and assistant message content blocks;
- plaintext `thinking` and opaque `redacted_thinking` blocks with explicit
  reasoning-visibility labels;
- tool calls and tool results, linked by tool-use ID when present;
- sidechain/subagent activity;
- compaction boundaries and summary records;
- incremental tails, truncation gaps, partial final records, and full
  reconciliation.

## Required before claiming complete Claude Code capture

- companion `tool-results` spill files;
- `file-history` snapshots and other session-linked files exposed locally;
- hook-driven wake-up hints plus periodic reconciliation;
- fixture updates when Claude Code changes its undocumented transcript schema.

Provider-hidden reasoning that is never written locally cannot be captured.
The ledger records this boundary rather than claiming false completeness.
