# Compatibility and evidence levels

Agent compatibility is not one boolean. This project reports four separate
levels so a parser test cannot be mistaken for a real Agent run.

1. **Format contract**: synthetic and frozen fixtures parse without loss and
   unsupported shapes become explicit gaps.
2. **Runtime startup**: the actual Agent executable starts and reports a
   version in a controlled environment.
3. **Native event path**: the runtime loads the integration and a native event
   reaches the verified local ledger.
4. **Provider-backed task**: an authenticated provider executes a task and the
   full supported evidence chain is captured.

A higher level does not imply access to provider-hidden reasoning.

## Current matrix

| Agent | Format contract | Runtime startup | Native event path | Provider-backed task |
| --- | --- | --- | --- | --- |
| Codex | Yes | Local probe plus replay-verifiable native process receipt | Rollout import and exact local execution-receipt path | Authenticated local tasks can be preserved, but provider and server-side model identity are not independently attested |
| Claude Code | Yes | CLI version probe only | Importer and hook protocol conformance | Not claimed; no equivalent native execution receipt exists |
| OpenCode | Yes | Pinned disposable hosted runner only | Hosted plugin event to verified ledger | Not claimed; the hosted smoke intentionally uses no provider key and OpenCode is not installed locally |
| DeepSeek Harness | Yes | Local `dsh` version probe | Community Bundle capture plus verbatim persistence backfill to the verified ledger | Not claimed by the compatibility command; a separate release smoke may exercise a configured provider but is not independent attestation |

The GitHub-hosted macOS job is a real macOS runner. It is not a physical-device
acceptance and is not described as one.

## Local compatibility command

```text
agentmem compatibility
agentmem compatibility --agent codex --agent claude-code
agentmem compatibility --agent opencode --require-all
agentmem compatibility --agent deepseek-harness
```

The command reports:

- history-import availability independently of executable startup;
- executable status: `available`, `blocked`, or `not_found`;
- normalized version output;
- executable SHA-256 when the file is readable;
- capture and retrieval modes shipped by this project;
- limitations that remain true even when the executable is available.

`available` means the version probe completed. It does not prove login state,
provider access, hook installation, native event capture, or task quality.
`--require-all` returns a nonzero exit status unless every selected executable
is available.
The default probe reads the executable once, content-addresses those bytes,
stages a private temporary copy, and runs that exact copy for its version output;
the reported hash therefore names the bytes that were executed.

On Windows, an app-execution alias can be readable and hashable while direct
version probing is blocked by application control. In that case runtime status
is `blocked`, but `history_import_available` remains true because importing an
existing transcript does not execute the Agent binary.

## Execution evidence modes

The compatibility report keeps executable availability separate from execution
evidence:

- Codex reports `local_replayable_receipt` only when the supplied evidence root
  contains a native receipt that passes complete replay.
- Claude Code reports `adapter_protocol_only`; fixtures and hook conformance do
  not become a claimed native task.
- OpenCode reports `hosted_runtime_smoke_only`; the pinned CI runtime is not a
  local installation and does not invoke a provider model.
- DeepSeek Harness reports `community_bundle_protocol_only`; executable startup
  does not prove the Bundle is installed, enabled in the selected profile, or
  used for a provider-backed task.

All four report `provider_independently_attested=false`. A native Codex receipt
authenticates the supported local process graph, not the remote provider,
server-side model, account, or provider-hidden reasoning. See
[native execution](native-execution.md).

## Codex

The importer accepts `rollout-*.jsonl` files and recursively discovers them in
a supplied directory. It preserves exact source segments and projects user and
agent messages, tool calls and results, approvals, exposed reasoning, summaries,
opaque encrypted reasoning, sub-Agent events, and compaction records.

Codex may expose only a summary or encrypted payload for some reasoning. Those
states are preserved and labelled; they are not expanded into invented text.

See `adapters/codex/CAPABILITIES.md` for the versioned contract.

`agentmem agent run codex` is a separate opt-in path. It uses an exact local
Codex executable, preserves raw JSONL and terminal evidence, and can consume an
exact verified loadout context. It does not install hooks or modify Codex
configuration.

`compatibility --root <evidence>` reports `native_execution_verified=true`
only when the evidence store contains a fully replayable native receipt whose
staged Codex executable SHA-256 equals the executable bytes probed by the
current command. A valid historical receipt for different Codex bytes remains
visible evidence but does not verify the current runtime.

## Claude Code

The Claude Code surface includes project transcripts, prompt history, companion
state, plaintext thinking blocks, opaque redacted-thinking blocks, tool use,
and unknown blocks. `claude-home` reconciles the supported home-directory
sources under one expected-source inventory.

The current public acceptance does not claim an authenticated Claude Code task.
Users should treat a new Claude Code release as unverified until the adapter
fixtures and a private local acceptance pass.

See `adapters/claudecode/CAPABILITIES.md`.

## OpenCode

OpenCode supports immutable export import and an opt-in TypeScript event plugin.
The GitHub-hosted runtime smoke test:

1. installs pinned OpenCode into the runner's temporary directory;
2. copies the plugin into a temporary project;
3. starts the real OpenCode server on loopback with authentication;
4. creates a native session without invoking a model;
5. verifies that the plugin writes a new `session.created` event whose session
   identifier matches the server response to its crash-safe spool;
6. imports the spool and requires `agentmem doctor` to pass;
7. deletes the temporary runtime and evidence.

The CI receipt publishes the event type and SHA-256 hashes of the matched event
and session identifier, never the raw identifier.

This proves runtime/plugin compatibility without requiring users to install
OpenCode and without placing a provider secret in CI. It does not prove a
provider-backed OpenCode task.

See `adapters/opencode/CAPABILITIES.md` and `integrations/opencode/README.md`.

## DeepSeek Harness

DeepSeek Harness support is an independently published community Bundle. The
capture path subscribes to the native `session/event` feed and writes exact
events to crash-safe local JSONL segments outside Git. At startup it also uses
`listSnapshots` and `readRaw` when the configured persistence backend exposes
verbatim per-session artifacts. Unsupported persistence, unknown events, and
read or projection failures remain explicit gaps.

Retrieval runs `agentmem inject deepseek-harness` from `agent/pre-step` and
appends a formal plugin-originated recall message only after Agent Memory
verifies the complete portable repository and current promoted revisions. A
missing CLI, timeout, bounded-output loss, malformed response, or verification
failure injects nothing and never reuses stale context.

This integration is not part of the capture supervisor, native execution
receipt path, or the fixed Codex/Claude Code/OpenCode continuous-evaluation
population. See `adapters/deepseekharness/CAPABILITIES.md` and
`integrations/deepseek-harness/README.md`.

## Version changes

When an Agent changes its local format:

- exact bytes remain authoritative;
- unknown shapes remain preserved;
- normalized coverage may decrease and must produce a gap or compatibility
  issue instead of a silent success;
- fixtures, capability documentation, and the compatibility matrix must be
  updated before the new version is claimed.
