# Codex adapter capability contract

Adapter version: `codex-jsonl/v1alpha1`.

## Source

The adapter imports Codex `rollout-*.jsonl` files. A file path imports one
explicit file; a directory path recursively imports matching rollout files.

Every newly observed byte range is first copied verbatim into the local
content-addressed blob store. Normalized events contain byte offsets and a
parent reference to that preserved source segment. Unknown top-level or payload
types remain available in the source segment and receive an `unknown` event
rather than being discarded.

## Normalized coverage

- user messages;
- assistant and agent messages;
- exposed reasoning events;
- reasoning summaries and opaque encrypted reasoning payloads;
- function and custom tool calls and outputs;
- MCP, patch, and web-search result events;
- approval, turn-context, token, world-state, and other system events;
- sub-agent communication and activity;
- compaction events and their replacement-history source bytes;
- session metadata and future unknown event types.

The source bytes are the evidence. Normalized classification is a versioned
derivation and can be rebuilt.

## Reasoning truth labels

- `event_msg/agent_reasoning` with local text is `raw_exposed`.
- `response_item/reasoning` with readable content is `raw_exposed`.
- readable summary without readable content is `summary_only`; any accompanying
  encrypted payload remains preserved in the raw source segment.
- encrypted content without readable content or summary is
  `encrypted_opaque`.
- absence of all three is `not_exposed`.

These labels describe the local artifact, not whether it equals a provider's
entire private chain-of-thought.

## Reconciliation behavior

- `agentmem capture hook codex` streams exact lifecycle-hook stdin into
  independent immutable local envelopes without a fixed input-size cutoff or a
  ledger scan.
- `agentmem capture reconcile codex` imports those envelopes and the configured
  rollout source; hook transcript paths are audited as hints and never followed
  outside that source.
- Default import resumes at the greatest committed byte offset.
- A terminated invalid JSON record becomes an explicit gap and does not block
  later records.
- A trailing partial record becomes an explicit retryable gap; its offset is
  not committed.
- Source truncation appends a gap before importing the new bytes from offset
  zero.
- `--full-reconcile` re-reads the complete source and deterministically skips
  unchanged records, allowing earlier in-place changes to be detected.
- Event IDs are deterministic for a source path, thread, byte range, and exact
  bytes, so restart and state-index loss do not duplicate unchanged events.

## Current safety limit

The ledger is single-writer in this milestone. Do not run concurrent import or
daemon processes against the same evidence root until cross-process locking and
crash-recovery tests are implemented. Default incremental import detects
append and truncation; detecting arbitrary earlier in-place modification
requires `--full-reconcile`.

Hook configuration and periodic execution are deployment choices. The CLI
does not install either one without explicit approval. Hook capture supplies
wake-up and compaction-boundary evidence; historical rollout reconciliation is
still required for complete locally available process evidence.
