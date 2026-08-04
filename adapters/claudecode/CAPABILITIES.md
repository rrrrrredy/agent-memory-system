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
- prompt history plus documented session companion artifacts through
  `agentmem import claude-home`, including spilled tool results, subagent
  transcripts, file-history snapshots, plans, tasks, debug logs, paste/image
  attachments, session metadata, and crash markers.

## Required before claiming complete Claude Code capture

- hook-driven wake-up hints plus periodic reconciliation;
- fixture updates when Claude Code changes its undocumented transcript schema.

Configuration, OAuth state, plugins, config backups, and generic caches are
outside the process-evidence allowlist. Adding any of them requires a separate
security decision.

Provider-hidden reasoning that is never written locally cannot be captured.
The ledger records this boundary rather than claiming false completeness.
