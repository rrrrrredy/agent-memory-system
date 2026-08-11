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
| Codex | Yes | Maintainer-reported private local acceptance | Maintainer-reported real rollout import | Minimal authenticated maintainer acceptance; not independently reproducible from this repository and not an independently certified evaluation bridge |
| Claude Code | Yes | CLI version probe only | Fixture and hook-contract coverage | Not claimed in the current acceptance record |
| OpenCode | Yes | Yes, pinned hosted runner | Yes, hosted plugin event to verified ledger | Not claimed; the hosted smoke test intentionally uses no provider key |

The GitHub-hosted macOS job is a real macOS runner. It is not a physical-device
acceptance and is not described as one.

## Local compatibility command

```text
agentmem compatibility
agentmem compatibility --agent codex --agent claude-code
agentmem compatibility --agent opencode --require-all
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

On Windows, an app-execution alias can be readable and hashable while direct
version probing is blocked by application control. In that case runtime status
is `blocked`, but `history_import_available` remains true because importing an
existing transcript does not execute the Agent binary.

## Codex

The importer accepts `rollout-*.jsonl` files and recursively discovers them in
a supplied directory. It preserves exact source segments and projects user and
agent messages, tool calls and results, approvals, exposed reasoning, summaries,
opaque encrypted reasoning, sub-Agent events, and compaction records.

Codex may expose only a summary or encrypted payload for some reasoning. Those
states are preserved and labelled; they are not expanded into invented text.

See `adapters/codex/CAPABILITIES.md` for the versioned contract.

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

## Version changes

When an Agent changes its local format:

- exact bytes remain authoritative;
- unknown shapes remain preserved;
- normalized coverage may decrease and must produce a gap or compatibility
  issue instead of a silent success;
- fixtures, capability documentation, and the compatibility matrix must be
  updated before the new version is claimed.
