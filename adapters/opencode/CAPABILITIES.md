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
that serializes OpenCode bus events to an append-only local JSONL spool. The
plugin is disabled unless `AGENT_MEMORY_OPENCODE_EVENT_LOG` is set. Capture
failures are reported but do not block the agent.

The plugin is not installed automatically. Installing it or modifying global
OpenCode configuration requires explicit user approval. Historical export
reconciliation remains required because a live hook can be absent, interrupted,
or unable to reconstruct state that predates its installation.

## Remaining M1 work

- crash-safe spool rotation and recovery;
- fixture updates when OpenCode changes its event or export schemas.

`agentmem capture opencode` enumerates native sessions and invokes unsanitized
exports into a non-Git staging directory. It preserves session-list output,
per-session stderr, manifests, failed or malformed stdout, and explicit failure
gaps before returning a non-zero status for partial capture.

Provider reasoning that never appears in a reasoning part cannot be recovered.
