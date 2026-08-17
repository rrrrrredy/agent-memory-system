# @rrrrrredy/dsh-agent-memory

Community DeepSeek Harness Bundle for [Agent Memory System](https://github.com/rrrrrredy/agent-memory-system). It can preserve Harness sessions as a crash-safe local append-only event spool and can inject only verified, active, promoted memory before an agent step.

This is a community plugin, not an official DeepSeek plugin.

## Compatibility

- DeepSeek Harness / `@deepseek-ai/dsh`: `0.1.0-rc.6`
- Agent Memory CLI: `>=0.3.1 <1` for `deepseek-harness` import and injection support
- Node.js: `22.19.x` or `24+`

Capture and retrieval are independent, explicit opt-ins. The default Bundle row is inert until at least `captureDir` or both retrieval roots are configured.

## Install

Install the Agent Memory CLI when retrieval or import is required:

```bash
go install github.com/rrrrrredy/agent-memory-system/cmd/agentmem@v0.3.1
```

Install the public Bundle into the Harness profile you use:

```bash
dsh plugin --profile headless add @rrrrrredy/dsh-agent-memory@0.1.0
dsh --profile headless --dump-config
```

For the browser surface, replace `headless` with `web`. For a local release candidate, replace the npm specifier with the absolute path to the packed `.tgz`.

## Configure

Update the `agent-memory` row in that profile's `cordis.patch.yml`. All storage paths must be absolute.

```yaml
- id: agent-memory
  config:
    binary: agentmem
    captureDir: /absolute/local/path/outside-any-git-worktree/dsh-capture
    evidenceRoot: /absolute/local/path/agent-memory-evidence
    portableRepo: /absolute/path/to/portable-memory-repository
    scopeRepository: account/example
    scopeProject: example
    scopeTask: agent-work
    limit: 5
    tokenBudget: 600
    byteBudget: 3072
    timeoutMs: 3000
    stdoutMaxBytes: 131072
    stderrMaxBytes: 131072
    segmentMaxBytes: 33554432
```

- Set only `captureDir` for capture without retrieval.
- Set `evidenceRoot` and `portableRepo` together for retrieval without capture.
- Set all three paths to compose both capabilities.
- Trusted repository, project, and task scopes come only from this local configuration. Model text cannot select them.

On Windows, use fully qualified absolute paths and escape backslashes when the YAML parser requires it. The capture directory is rejected if it is inside a Git worktree, including through a linked path.

## Use

Run Harness normally:

```bash
dsh --profile headless "Continue this repository task using only relevant verified memory."
```

At the first accepted step of each turn, the Bundle sends only user-originated text to:

```bash
agentmem inject deepseek-harness --root <evidenceRoot> --repo <portableRepo> ...
```

A successful bounded response becomes a formal `user/message` whose source is `plugin: agent-memory` and form is `recall`, so it remains visible and replayable in the Harness event log. Missing CLI, timeout, output overflow, nonzero exit, damaged JSON, failed verification, or an empty result all continue without memory. The Bundle never falls back to stale, partial, unverified, or cached context.

Import the local capture spool into the Agent Memory evidence ledger explicitly:

```bash
agentmem import deepseek-harness-events \
  --root <local-evidence-directory> \
  --path <captureDir>
```

Live `session/event` records are retained exactly as delivered by Harness. On startup, a raw-artifact-capable persistence backend is also read through `listSnapshots` and `readRaw`; its full session artifact is preserved verbatim. Unknown events, unsupported raw artifacts, malformed artifact rows, recovery problems, and read failures remain explicit gaps rather than disappearing.

## Privacy and security boundary

- Capture files are local-only raw evidence and may contain prompts, model output, locally exposed reasoning, tool calls, tool results, paths, and secrets produced during the session.
- Raw Harness events, reasoning, tool output, and session artifacts must never be committed to Git, packed into npm, or attached to a GitHub Release.
- The Bundle adds no telemetry, hosted service, remote storage, or model tool. Harness still sends ordinary conversation content to the model provider configured for the profile.
- Retrieval returns only memory that Agent Memory validates as active and promoted from a complete portable repository. Capture does not automatically promote anything.
- `raw_exposed` means the text existed in the local Harness artifact. It does not claim access to provider-hidden reasoning.
- Local files are not an OS security boundary. A process with the same user privileges can alter local configuration or evidence.

## Uninstall

```bash
dsh plugin --profile headless remove @rrrrrredy/dsh-agent-memory
dsh --profile headless --dump-config
```

After removal, the Bundle row and package reference are absent, no event or pre-step listener remains, and later sessions do not add capture records or memory recall messages. Existing local capture and evidence files are deliberately retained; remove them separately only under your own retention policy.

## Reproduce the tests

From this directory:

```bash
pnpm install --frozen-lockfile
pnpm verify
AGENTMEM_BINARY=/absolute/path/to/agentmem pnpm test:integration
pnpm pack --pack-destination .pack
```

`pnpm verify` covers config boundaries, canonical-source hashing, Git-worktree rejection, durable raw event capture, raw-artifact backfill and restart deduplication, explicit gaps, argv-only subprocess execution, timeouts, output bounds, malformed responses, trusted prompt selection, formal recall messages, real Cordis/SessionStore/JSONL Persistence composition, and clean disposal.

`pnpm test:integration` writes a real Bundle spool into an explicit scratch root, imports it with the compiled Agent Memory CLI, and requires a clean `agentmem doctor` result. Set `AGENT_MEMORY_DSH_TEST_TMP` to keep all scratch work on a chosen volume.

The public release evidence records the separate isolated-profile tarball install/remove test and real DeepSeek model smoke. Raw session and memory evidence are excluded from the package and release assets.
