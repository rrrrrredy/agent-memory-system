# ADR 0001: Go core with thin native adapters

- Status: accepted for the first implementation slice
- Date: 2026-08-04

## Context

The product needs one local CLI and daemon-capable core on Windows and macOS,
while Codex, Claude Code, and OpenCode expose different files, events, hooks, and
plugin surfaces. The evidence format must remain usable even if any particular
integration changes.

## Decision

Use Go for the core CLI, evidence ledger, verification, episode reconstruction,
promotion workflow, synchronization orchestration, and local retrieval service.

Keep each Agent adapter thin:

- parse or subscribe to the Agent's native local source;
- preserve exact source bytes before normalization;
- emit versioned protocol events;
- report unsupported or missing fields explicitly;
- never decide that a candidate is valid memory.

An adapter may use TypeScript or another native plugin language when required by
the host, but it communicates with the core through versioned JSON events or a
local process protocol. The core is not embedded in an Agent plugin.

Expose MCP later as a consumer-facing retrieval interface, not as the evidence
store.

## Why

- A single Go binary lowers installation and recovery complexity across Windows
  and macOS.
- The standard library is sufficient for the initial content-addressed store,
  append-only ledger, and integrity checks.
- Host-native adapter code can change independently without changing canonical
  storage.
- The protocol remains inspectable and testable without an LLM.

## Rejected alternatives

- **One TypeScript service for everything:** good plugin ergonomics, but couples
  the local core to a JavaScript runtime and does not remove native adapter work.
- **Python-first core:** strong evaluation ecosystem, but packaging a durable
  cross-platform collector is less predictable. Python remains suitable for
  offline evaluation tooling.
- **Agent-specific hooks as the core:** cannot provide shared truth or recovery
  across three Agents.
- **A remote database service:** conflicts with the local-first privacy and
  offline requirements.

## Consequences

- Adapter contracts and golden fixtures need first-class versioning.
- Go cannot recover data absent from a provider API or local artifact.
- SQLite and embeddings may be added only as rebuildable local indexes.
- A daemon/watch mode requires explicit concurrency and crash-recovery tests
  before it can replace manual import.
