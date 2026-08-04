# Cross-Agent retrieval

Retrieval reads only active, reviewed, redacted memory from the separate portable
repository. It never searches raw evidence or candidate experience during an
ordinary Agent task.

## Verification and selection

Every request verifies the whole portable repository before selecting a memory.
An invalid revision, unexpected file, fork, conflict, or secret finding returns
no memory. The evidence root and portable repository must also resolve to
physically separate trees. Exact `memory_id` reads use the same verification and
scope checks as search.

Search is deterministic and offline. The current baseline tokenizes Unicode
words and CJK bigrams, requires a lexical match, applies scope and evidence
specificity, and uses stable tie-breaking. There is no zero-match fallback.

The caller must provide its Agent identity. Optional repository, project, and
task values are trusted logical identifiers, not filesystem paths. They should
come from local configuration rather than prompt text. A scoped memory is
eligible only for the exact configured value. Global memory remains eligible
for every Agent.

Three budgets apply independently:

- result count;
- estimated tokens;
- UTF-8 bytes.

A result that cannot fit is excluded rather than truncated into a misleading
fragment. `recall context` escapes structural markup and renders the selected
revisions into one bounded block that states current instructions and repository
rules take precedence.

## CLI

```text
agentmem recall search \
  --root <local-evidence-directory> \
  --repo <portable-memory-directory> \
  --agent codex \
  --scope-repository <logical-repository-id> \
  --query "current task terms"

agentmem recall get \
  --root <local-evidence-directory> \
  --repo <portable-memory-directory> \
  --agent codex \
  --memory <memory-id>

agentmem recall context \
  --root <local-evidence-directory> \
  --repo <portable-memory-directory> \
  --agent codex \
  --query "current task terms" \
  --limit 5 \
  --token-budget 600 \
  --byte-budget 3072 \
  --output text

agentmem recall verify --root <local-evidence-directory>
```

`search` records what was considered and selected. `context` additionally
records the exact delivered text. These local receipts include the raw query and
local repository identity and never enter the portable Git repository.

## MCP

Run one local stdio server for the Agent and trusted logical scope:

```text
agentmem serve mcp \
  --root <local-evidence-directory> \
  --repo <portable-memory-directory> \
  --agent codex \
  --scope-repository <logical-repository-id>
```

Use `claude_code` or `opencode` for the other Agents. Register the command as a
local stdio MCP server using that Agent's supported configuration. The server
exposes:

- `memory_search`: structured ranked results;
- `memory_get`: one exact active revision;
- `memory_context`: a bounded model-visible block plus structured results.

MCP tool arguments may provide thread and session identifiers as caller-reported
local provenance only when startup configuration omitted them. Configured
values take precedence, and caller labels cannot override repository, project,
or task scope. The tools are non-destructive, but not read-only in the strict
protocol sense because they append local receipts.

## Optional Codex and Claude Code hooks

Both Agents can invoke the same stdin/stdout bridge on `UserPromptSubmit`:

```text
agentmem inject codex \
  --root <local-evidence-directory> \
  --repo <portable-memory-directory> \
  --scope-repository <logical-repository-id>

agentmem inject claude-code \
  --root <local-evidence-directory> \
  --repo <portable-memory-directory> \
  --scope-repository <logical-repository-id>
```

The Agent sends its documented hook JSON on stdin. The bridge preserves that
exact invocation locally, uses only the prompt and stable metadata, and returns
`hookSpecificOutput.additionalContext`. It hashes but never opens
`transcript_path`; historical transcript capture remains the responsibility of
the reconciliation adapters.

The bridge caps input at 4 MiB and context at 600 estimated tokens and 3072
UTF-8 bytes by default. Oversize, malformed, unavailable, or invalid-repository
input yields `continue: true` without memory and records an explicit gap when the
evidence ledger is available. Hook payloads over 64 KiB are retained as
content-addressed local blobs instead of oversized ledger lines. Installing a
hook or changing Agent configuration requires explicit approval and is not
performed by `agentmem`.

Current hook contracts are documented by
[Codex lifecycle hooks](https://developers.openai.com/codex/hooks) and
[Claude Code hooks](https://code.claude.com/docs/en/hooks).

## Optional OpenCode plugin

`integrations/opencode/agent-memory-retrieval.ts` is an opt-in plugin source. It
uses `opencode-retrieval.mjs` to spawn `agentmem` directly without a shell. Set:

```text
AGENT_MEMORY_EVIDENCE_ROOT=<local-evidence-directory>
AGENT_MEMORY_PORTABLE_REPO=<portable-memory-directory>
AGENT_MEMORY_BINARY=<absolute-agentmem-executable>
AGENT_MEMORY_SCOPE_REPOSITORY=<logical-repository-id>
```

Optional variables are `AGENT_MEMORY_SCOPE_PROJECT`,
`AGENT_MEMORY_SCOPE_TASK`, `AGENT_MEMORY_RETRIEVAL_LIMIT`,
`AGENT_MEMORY_RETRIEVAL_TOKEN_BUDGET`,
`AGENT_MEMORY_RETRIEVAL_BYTE_BUDGET`, and
`AGENT_MEMORY_RETRIEVAL_TIMEOUT_MS`.

On a real user message, the plugin caches only that prompt and its message ID.
Each `experimental.chat.system.transform` call performs a fresh verified
retrieval immediately before appending the context, so every actual model-call
injection has its own local receipt and token accounting. A failed retrieval
injects nothing; no recalled context is cached for reuse. Before compaction, the
plugin repeats the verified retrieval from the latest real user prompt and adds
the result through `experimental.session.compacting`. Synthetic text parts are
ignored and the prompt cache is bounded.

These OpenCode injection hooks are experimental and require a compatible
OpenCode version. The plugin is disabled when either required root is absent and
fails open on executable errors, timeouts, or malformed output. The common MCP
server remains the preferred fallback. OpenCode's current plugin and compaction
contracts are documented in the
[official plugin guide](https://opencode.ai/docs/plugins/).

The retrieval plugin does not replace
`agent-memory-evidence.ts`, native unsanitized exports, or historical
reconciliation. No plugin is installed automatically.

## Adoption and outcomes

Retrieval is not proof that an Agent used a memory, and use is not proof that the
memory helped. Record those observations separately with a request matching
`schemas/memory-adoption-request.schema.json`:

```text
agentmem recall adoption \
  --root <local-evidence-directory> \
  --agent codex \
  --file <adoption-request.json>
```

Every item must reference a revision selected by the named retrieval or
injection receipt. A memory that was not adopted cannot be assigned a task
outcome. Agent and harness claims of helpful, neutral, or harmful results require
complete, hash-verified `tool_result` or `file_change` events; retrieval,
injection, adoption, and Agent-message events cannot prove effectiveness. The
result event must follow the referenced delivery and belong to the same task
context. A human report is retained as a direct attestation. Reporter kinds are
`human`, `agent`, and `harness`.

`recall verify` checks receipt integrity, event ordering, content hashes,
budgets, and reference relationships. A retrieval without a later outcome is
reported as open, not corrupt.
