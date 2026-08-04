# Continuous local capture

Codex and Claude Code use the same two-stage capture path:

1. A lifecycle hook streams its exact stdin into a durable local envelope.
2. Reconciliation imports those envelopes and the Agent's locally available
   historical sources into the evidence ledger.

The hook hot path never scans the ledger and never opens a transcript. This
keeps `SessionEnd`, prompt, compaction, and subagent hooks independent of ledger
size. Each invocation is committed as a separate immutable JSONL envelope
under the local evidence root. The original bytes are recoverable from a
base64 field and bound to their byte count and SHA-256 digest. Input is streamed
without a fixed transcript-size cutoff.

OpenCode keeps its native export plus append-only plugin path described in
[`adapters/opencode/CAPABILITIES.md`](../adapters/opencode/CAPABILITIES.md).

## Hook commands

Use these commands as command-hook handlers:

```text
agentmem capture hook codex --root <local-evidence-directory>
agentmem capture hook claude-code --root <local-evidence-directory>
```

Copy and edit the appropriate configuration fragment only after approval:

- [`integrations/codex/capture-hooks.example.json`](../integrations/codex/capture-hooks.example.json)
- [`integrations/claude-code/capture-hooks.example.json`](../integrations/claude-code/capture-hooks.example.json)

Both commands always return valid fail-open hook JSON:

```json
{"continue":true}
```

A successful capture is durable before the command exits. A read interrupted
after some bytes were received commits a `partial` envelope instead of erasing
the prefix. A destination failure is written to stderr while the Agent remains
unblocked; `doctor` and reconciliation make persisted failures observable.

Use hook events as wake-up and boundary evidence, not as a substitute for the
transcript. Recommended events are:

- `SessionStart` and `SessionEnd`;
- `UserPromptSubmit`;
- `PreCompact` and `PostCompact`;
- `SubagentStop`;
- `Stop`.

Tool-call hooks may also be captured, but transcript reconciliation remains the
authoritative way to retain model messages, hosted-tool activity, tool output,
and events produced while hooks were unavailable.

The current upstream contracts are documented by
[Codex lifecycle hooks](https://developers.openai.com/codex/hooks) and
[Claude Code hooks](https://code.claude.com/docs/en/hooks). `agentmem` does not
install either configuration. Enabling a hook still requires explicit user
approval.

## Reconciliation

Run reconciliation at session boundaries and periodically:

```text
agentmem capture reconcile codex \
  --root <local-evidence-directory> \
  --path <codex-rollout-file-or-sessions-directory>

agentmem capture reconcile claude-code \
  --root <local-evidence-directory> \
  --path <claude-code-home-directory>
```

Codex reconciliation imports matching `rollout-*.jsonl` files. Claude Code
reconciliation imports project and subagent transcripts, prompt history,
spilled tool results, file history, plans, task state, debug records,
attachments, and the other allowlisted companion artifacts documented by its
adapter.

The default mode resumes each source at its last committed byte. Add
`--full-reconcile` to re-read complete sources and detect earlier in-place
changes.

Hook-provided `transcript_path` and `agent_transcript_path` values are hints
only. Reconciliation never follows them directly. It imports only the
operator-configured source tree, reports hints outside that tree, and reports
missing or non-regular hinted files as explicit local gaps. This prevents a
hook payload from expanding the evidence allowlist.

Malformed envelopes, partial stdin, missing transcript paths, unavailable
source trees, permission failures, Claude Code source warnings, and rejected
path hints are retained as local gap evidence. The result is non-zero when the
configured historical source could not be reconciled.

## Verification and recovery

`agentmem doctor` validates both the evidence hash chain and every durable hook
envelope. It reports complete and partial envelope counts separately. Envelopes
remain inside the local evidence root, are included in encrypted evidence
backups, and are never eligible for portable-memory export or Git sync.

The evidence ledger remains single-writer. Do not run concurrent reconciliation,
derivation, retrieval-receipt, or evaluation writers against one store. Hook
capture itself is concurrency-safe because it writes independent immutable
envelopes and does not acquire the ledger writer lock.
