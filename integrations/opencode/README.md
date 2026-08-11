# OpenCode integration

This directory contains two opt-in OpenCode plugins:

- `agent-memory-evidence.ts` writes native OpenCode events to a crash-safe local
  JSONL spool;
- `agent-memory-retrieval.ts` requests bounded portable memory and receipts each
  actual injection.

Neither plugin is installed automatically. The event spool must remain outside
every Git worktree and outside the portable memory repository.

## Evidence capture

Copy these files into a project's `.opencode/plugins` directory:

```text
agent-memory-evidence.ts
crash-safe-jsonl.mjs
```

Set an absolute local spool path before starting OpenCode:

```text
AGENT_MEMORY_OPENCODE_EVENT_LOG=<outside-git>/events.jsonl
```

The plugin creates PID-tagged immutable segments, serializes concurrent writes,
and preserves a partial tail before recovery. Capture failures are reported but
do not block the Agent.

Import the spool explicitly:

```text
agentmem import opencode-events \
  --root <local-evidence-directory> \
  --path <spool-directory>
```

## Retrieval

Copy these files into `.opencode/plugins`:

```text
agent-memory-retrieval.ts
opencode-retrieval.mjs
```

The bridge configuration is opt-in and uses the same verified portable memory
repository as Codex and Claude Code. See `docs/retrieval.md` for environment
variables, scope rules, failure behavior, and receipts.

## Tests

The local package tests do not install OpenCode:

```text
pnpm install --frozen-lockfile --ignore-scripts
pnpm test
pnpm typecheck
```

GitHub CI separately installs pinned `opencode-ai` into a disposable hosted
runner, starts its authenticated loopback server, creates a native session,
requires this plugin to capture the event, imports the spool, and verifies the
evidence ledger. The smoke test invokes no provider model and uses no model
secret.

## Privacy

The spool contains raw native events and is local evidence. Do not commit it,
attach it to an issue, or place it in the private portable-memory repository.
Only reviewed, redacted, promoted memory may cross the Git boundary.
