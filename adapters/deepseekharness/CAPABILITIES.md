# DeepSeek Harness adapter capabilities

Adapter version: `deepseek-harness-event-jsonl/v1alpha1`

The adapter imports the crash-safe, append-only JSONL written by the optional
DeepSeek Harness community Bundle in this repository. Exact source segments are
preserved in the local evidence store before projection.

Supported projections include user and assistant messages, locally exposed
reasoning chunks, tool calls and results, approvals, compaction, subagent
boundaries, and core turn/step/request events. Unknown event types, malformed
records, unsupported raw-artifact backends, and failed persistence reads become
explicit gaps; they are never silently discarded.

The Bundle may also capture a persistence backend's verbatim per-session
artifact. Those artifact bytes remain local-only. Repeated live/backfill copies
of the same session event are deduplicated by session id, event sequence, and
the canonical event content digest. The default JSONL backend's `text-chunks`,
`reasoning-chunks`, and `tool-call-chunks` storage rows are validated and
expanded with the upstream lossless mapping before projection; a malformed
packed row becomes one explicit gap while the verbatim artifact remains intact.

Reasoning is labelled `raw_exposed` only when reasoning text exists in the
locally available Harness event. The adapter does not claim access to
provider-hidden reasoning.

Capture is opt-in and must target a directory outside every Git worktree,
including linked aliases. Importing does not install or enable the Bundle.
