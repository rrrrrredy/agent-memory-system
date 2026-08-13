# Replay-verifiable native Codex execution

The native bridge runs an exact local Codex CLI process and preserves enough
local evidence to replay the supported execution claim. It is intended for
diagnostics and prospective studies that need stronger evidence than a caller
saying a task ran.

It does not authenticate the remote provider, selected server-side model, or
provider-hidden reasoning. Its authority is deliberately limited to:

```text
local_codex_cli_process; remote_provider_model_and_private_reasoning_not_independently_attested
```

## Request

Keep the request outside Git. `working_directory` must be an existing absolute
path.

```json
{
  "schema_version": "native-agent-run-request/v1alpha1",
  "task_id": "release-check-001",
  "prompt": "Inspect the repository and report whether the documented check passes.",
  "model": "default",
  "sandbox": "read-only",
  "working_directory": "<absolute-repository-path>",
  "timeout_seconds": 300,
  "skip_git_repository_check": false,
  "privacy": "local_only"
}
```

Run and verify:

```text
agentmem agent run codex \
  --root <local-evidence-directory> \
  --file <native-run-request.json> \
  --codex <absolute-codex-executable>

agentmem agent verify --root <local-evidence-directory>
```

For a memory-backed task, first create a verified loadout context and add its
receipt ID to the request:

```json
"loadout_context_receipt_id": "memory-loadout-context-<sha256>"
```

Then pass `--repo <portable-memory-directory>`. The bridge refuses a portable
repository without a loadout receipt and refuses a receipt whose Agent, task
scope, loadout, or exact active revisions no longer match. For loadout-backed
runs it holds the portable repository use lock from delivery verification until
the terminal receipt is written, so supersession, revocation, and export cannot
interleave after a stale-head check.

## Preserved evidence

Before execution the bridge content-addresses the exact Codex executable and
the current `agentmem` runner executable, stores the canonical request and
prompt, resolves the working directory, records the complete argument vector,
and hashes the names of inherited environment variables without recording
their values. It stages and re-hashes the bound executable bytes before use.

After execution it preserves raw Codex JSONL, stderr when present, the final
Agent message, thread identifier, usage fields, tool-call count, process exit
code, timing, reasoning visibility, and a terminal completed or failed
receipt. A timeout, start failure, malformed stream, missing terminal message,
or nonzero process exit remains a non-retryable local failure receipt.

`agent verify` replays event order, parentage, record hashes, content-addressed
blobs, executable bindings, request normalization, command arguments, raw JSONL
parsing, usage, output, outcome, and receipt identity.

## Trust and compatibility boundaries

- Local filesystem and process evidence assumes the operating system and local
  operator are not fully compromised.
- Provider-private chain-of-thought is never recovered or inferred.
- The request uses `--ephemeral`, `--json`, `--ignore-user-config`, and
  `--ignore-rules`; it does not modify Codex configuration.
- `read-only` is the recommended sandbox for verification-only tasks.
- Codex has a locally replayable process receipt. Claude Code currently has
  protocol conformance only. OpenCode is exercised only by the disposable
  GitHub-hosted runtime smoke and is not installed locally by this project.
- A completed local receipt is still not independent provider or model
  attestation and cannot by itself certify learning efficacy.

The public request, start, receipt, result, and verification contracts are
versioned under `schemas/native-agent-*.schema.json`.
