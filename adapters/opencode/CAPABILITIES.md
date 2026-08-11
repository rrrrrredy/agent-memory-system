# OpenCode adapter capability contract

OpenCode session state remains owned by OpenCode. The adapter never treats the
live SQLite database as portable memory and never places it in Git.

## Historical reconciliation

Run OpenCode's native `export` command without `--sanitize`, then import the
resulting JSON document. The adapter preserves the exact export before parsing
its session, message, and part objects. It currently projects:

- user and assistant text;
- reasoning with explicit visibility;
- tool input, output, errors, and correlation IDs;
- files, snapshots, patches, subtask/agent activity, retries, and compaction;
- message and session metadata, including unknown future part types.

An export containing OpenCode's redaction markers is recorded as partial and
produces an explicit gap. It is not accepted as complete process evidence.

## Live capture

`integrations/opencode/agent-memory-evidence.ts` is an optional plugin source
that serializes OpenCode bus events to append-only local JSONL segments. The
plugin and `crash-safe-jsonl.mjs` must be deployed together. It is disabled
unless `AGENT_MEMORY_OPENCODE_EVENT_LOG` names an outside-Git `.jsonl` base path.
Writer-specific segments are created beside that path. Each record is synced
before the hook returns. Rollover starts a new immutable path instead of
renaming an existing segment, so prior imports are not replayed under a new
identity. `AGENT_MEMORY_OPENCODE_EVENT_MAX_BYTES` optionally overrides the
32 MiB segment limit.

On startup, an unterminated tail is copied to a content-addressed
`.partial.jsonl` recovery artifact before the source segment is truncated to its
last complete record. Both the complete segments and recovery artifact are
accepted by `import opencode-events`; the latter becomes an explicit gap.
Capture and serialization failures are reported but do not block the agent.
Recovery failures leave the original segment untouched, are reported, and do
not prevent capture from continuing in a new segment. The writer refuses any
destination inside a Git worktree, including a path that enters one through a
directory link.

The plugin is not installed automatically. Installing it or modifying global
OpenCode configuration requires explicit user approval. Historical export
reconciliation remains required because a live hook can be absent, interrupted,
or unable to reconstruct state that predates its installation.

## Optional promoted-memory injection

`integrations/opencode/agent-memory-retrieval.ts` retrieves only verified active
revisions from the separate portable memory repository. It runs `agentmem`
directly without a shell, accepts trusted logical scopes only from local
environment configuration, and enforces bounded output. A missing executable,
timeout, invalid repository, or malformed response produces no injected memory
and does not block OpenCode.

The plugin retrieves on `chat.message`, injects through
`experimental.chat.system.transform`, and refreshes the latest real user prompt
through `experimental.session.compacting`. These OpenCode hooks are
version-sensitive and experimental; the shared MCP server is the stable
fallback. The compatibility baseline is type-checked against
`@opencode-ai/plugin` 1.18.11. The plugin is disabled unless both the local
evidence root and portable repository are configured. It is never installed
automatically and does not replace event capture or historical reconciliation.
See `docs/retrieval.md`.

`agentmem capture opencode` enumerates native sessions and invokes unsanitized
exports into a non-Git staging directory. It preserves session-list output,
per-session stderr, manifests, failed or malformed stdout, and explicit failure
gaps before returning a non-zero status for partial capture.

Provider reasoning that never appears in a reasoning part cannot be recovered.
