# Agent Memory System

[![CI](https://github.com/rrrrrredy/agent-memory-system/actions/workflows/ci.yml/badge.svg)](https://github.com/rrrrrredy/agent-memory-system/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

Local-first, evidence-backed memory for coding agents.

Agent Memory System preserves the task evidence a runtime exposes, rebuilds it
into episodes, turns supported observations into reviewable memory candidates,
and shares only redacted memories with explicit validation and promotion
records through a separate private Git repository. Deployment policy requires
an operator to make those decisions; the software binds each attestation to
the evidence but does not authenticate that the caller is human.

It is built for a harder question than “what should the agent remember?”:

> What evidence supports this memory, is it still current, and can another
> machine use it without receiving the private transcript?

Version **0.3.0** adds immutable memory loadouts, evidence-bound review packets,
replay-verifiable local Codex execution receipts, prospective longitudinal
study records, and a loopback-only read-only dashboard to the storage,
derivation, review, private Git, retrieval, recovery, and evaluation foundation.
Runtime adapters remain version-sensitive. The system fails closed when
evidence is incomplete or a compatibility claim cannot be verified.

## Why this is different

Most memory tools summarize a conversation and place the summary in a retrieval
index. This project separates evidence, judgment, and distribution:

```mermaid
flowchart LR
    A["Agent-local traces"] --> B["Append-only evidence ledger"]
    B --> C["Episodes and compaction checks"]
    C --> D["Candidate memories"]
    D --> E["Validation attestation"]
    E --> F["Promote, supersede, or revoke"]
    F --> G["Private Git memory repository"]
    G --> H["Bounded cross-agent retrieval"]
    H --> I["Immutable memory loadouts"]
    I --> J["Native receipts and prospective studies"]
```

Raw transcripts, tool output, exposed reasoning, and local paths remain in the
local evidence store. Git receives only the portable projection of an approved
memory. A private repository is not treated as permission to upload raw task
history.

## Core guarantees

- Exact source bytes are content-addressed before normalization.
- Every evidence record participates in an append-only SHA-256 chain.
- A cross-process writer lock prevents compliant writers from forking a store.
- Missing, truncated, opaque, or unavailable data becomes an explicit gap.
- Candidates cannot become portable memory without a validation attestation and
  a separate promotion attestation, each bound to the exact evidence state.
- Conflicting candidates are quarantined; cross-device semantic conflicts are
  reported instead of resolved with last-write-wins.
- Reviewer and approver identifiers are caller-supplied attestations. Operator
  policy requires a person to inspect the evidence, but the CLI does not
  authenticate a human identity.
- Active memories form immutable revision chains with explicit supersession and
  revocation.
- Retrieval verifies the complete portable repository, applies exact scope and
  result limits plus estimated-token and UTF-8 byte budgets, and records what
  was delivered.
- A memory loadout names exact active revision heads. Supersession or revocation
  makes the old loadout stale instead of silently substituting new content.
  Loadout-backed native execution holds the portable repository use lock from
  freshness verification through the terminal receipt.
- Review packets freeze the displayed candidate text, provenance, generation,
  and evidence prefix without combining validation and promotion authority.
- Native Codex receipts bind exact local executable bytes, request, JSONL,
  output, usage, and terminal state; they do not authenticate the remote
  provider, server-side model, or hidden reasoning.
- Prospective studies seal every request, acceptance assertion, and workspace
  snapshot before execution. A single-use study reservation is written before
  Codex starts; failures consume it, and outcomes replay the complete
  Agent-message bytes. Reports remain descriptive rather than causal.
- Raw evidence backup is optional, encrypted with age, and completely separate
  from the readable memory repository.
- `AGENTS.md`, Skills, and other rule surfaces are never changed without a
  distinct, target-bound approval.

These guarantees apply to locally available evidence. No client can recover
provider-hidden reasoning or bytes that the runtime never exposed.

## Install

The project can be built from source with Go 1.25 or newer.

Windows PowerShell:

```powershell
git clone https://github.com/rrrrrredy/agent-memory-system.git
cd agent-memory-system
New-Item -ItemType Directory -Force .\bin | Out-Null
go build -o .\bin\agentmem.exe .\cmd\agentmem
.\bin\agentmem.exe version
```

macOS or Linux:

```sh
git clone https://github.com/rrrrrredy/agent-memory-system.git
cd agent-memory-system
mkdir -p ./bin
go build -o ./bin/agentmem ./cmd/agentmem
./bin/agentmem version
```

Tagged releases provide checksum-verified Windows, macOS, and Linux archives;
the supported installers target Windows and macOS. See [install, upgrade, and
uninstall](docs/install.md).

## Try the complete lifecycle

A privacy-safe fixture under `examples/quickstart` exercises the same path as
real Codex history. One command imports, derives, verifies, and reports the next
operator action:

```powershell
$Evidence = Join-Path ([System.IO.Path]::GetTempPath()) ("agentmem-" + [guid]::NewGuid())
$Onboard = .\bin\agentmem.exe onboard codex --root $Evidence --path .\examples\quickstart | ConvertFrom-Json
.\bin\agentmem.exe status --root $Evidence
.\bin\agentmem.exe review list --root $Evidence --status review_ready --limit 20
```

```sh
evidence="$(mktemp -d)/evidence"
./bin/agentmem onboard codex --root "$evidence" --path ./examples/quickstart
./bin/agentmem status --root "$evidence"
./bin/agentmem review list --root "$evidence" --status review_ready --limit 20
```

Review and promotion remain separate explicit decisions; `onboard` never makes
either decision. The [quickstart](docs/quickstart.md) provides complete
PowerShell and POSIX commands for review, promotion, private Git export, exact
retrieval, and switching to an existing Codex sessions directory.

A runtime probe is optional and never gates history import:

```powershell
.\bin\agentmem.exe compatibility --agent codex
```

```sh
./bin/agentmem compatibility --agent codex
```

`history_import_available` remains separate from `runtime_status`, so a blocked
version probe does not hide a working offline importer.

## Compatibility and verification

| Surface | Current evidence |
| --- | --- |
| Codex rollout import | Maintainer-reported private real-rollout acceptance on Windows, plus public loss/compaction fixtures and cross-platform protocol tests; the private run is not independently reproducible from this repository |
| Claude Code import | Transcript, history, companion, thinking, and unknown-block fixtures on Windows, macOS, and Linux |
| OpenCode plugin | Pinned OpenCode runtime starts on a GitHub-hosted runner, loads the plugin, captures the matching `session.created` event, imports it, and verifies the ledger; no provider model or secret is used |
| DeepSeek Harness Bundle | Real Cordis, SessionStore, JSONL Persistence, and `agent/pre-step` composition tests cover verbatim backfill, crash-safe live capture, verified-memory injection, real CLI import, and clean unload; this is community-Bundle evidence, not DeepSeek certification |
| Review and memory lifecycle | Evidence-bound caller attestations, promotion, supersession, revocation, secret scanning, and conflict tests; caller identity is not authenticated |
| Cross-device memory | Private Git history verification, offline use, divergence handling, recovery, and hosted Windows/macOS/Linux tests |
| Native Codex memory diagnostic | A frozen 20-cluster synthetic suite ran 20 paired authenticated `codex exec` tasks with sealed no-tools policy and verified retrieval injections: 19 wins, 1 tie, 0 losses; baseline 1/20, memory 20/20; one-sided sign-test p=0.0000019073. This is bounded capability evidence, not longitudinal certification; see the [aggregate receipt](evals/results/codex-memory-capability-v1-2026-08-12.json) |
| Native Codex process receipts | Exact local Codex and runner bytes, request, arguments, raw JSONL, usage, output, and terminal state are replayed; provider, model, and private reasoning are not independently attested |
| Portable loadouts | Content-addressed exact revision sets, scope, Agent allowlist, budgets, stale-head rejection, and composite delivery receipts |
| Prospective studies | Pre-sealed requests and workspace snapshots, immutable Agent-message acceptance hashes, deterministic balanced assignment, one single-use study-bound attempt, and replay-derived outcomes; no completed real-world longitudinal efficacy claim is made |
| Learning efficacy | Frozen-corpus metrics and replay are implemented; efficacy certification remains blocked unless task execution is independently verified |

Run a local, non-mutating runtime probe:

```text
agentmem compatibility
```

The probe checks executable availability and version output. It does not claim
that an account is authenticated, that a provider ran a model, or that a native
session was captured. See [compatibility and evidence levels](docs/compatibility.md).

## Memory lifecycle

A portable memory is not an editable note. It is an immutable revision chain:

```text
promote -> active revision
active revision -> supersede -> new active revision
active revision -> revoke -> tombstone
```

Only the current, non-revoked head can be retrieved. Two roots, two children of
one parent, an altered historical file, or opposing active memories make
verification fail. See [promotion](docs/promotion.md) and
[portable memory](docs/portable-memory.md).

## Retrieval

Retrieval is deterministic and offline. It combines exact identity lookup,
Unicode word and CJK-bigram matching, scope specificity, evidence strength, and
stable tie-breaking. There is no zero-match fallback.

Use the CLI directly or expose the same verified search through a local stdio
MCP server:

```text
agentmem recall search --root <evidence> --repo <private-memory> --agent codex --query "release checks"
agentmem serve mcp --root <evidence> --repo <private-memory> --agent codex
```

Codex, Claude Code, OpenCode, and DeepSeek Harness consume the same portable
protocol. Their native hooks, plugins, or Bundles are optional; CLI and MCP
remain the stable boundary. The DeepSeek Harness community Bundle is documented
under [`integrations/deepseek-harness`](integrations/deepseek-harness).

## Operational memory

Create a reusable, exact set of promoted revisions:

```text
agentmem loadout create --repo <private-memory> --name "Release checks" \
  --scope-kind project --scope-value example-project --agent codex \
  --memory <memory-id>
agentmem loadout context --root <evidence> --repo <private-memory> \
  --loadout <loadout-id> --agent codex --scope-project example-project
```

Build one immutable review surface before promotion:

```text
agentmem review packet --root <evidence> --status review_ready --limit 20
agentmem promote candidate --root <evidence> --candidate <candidate-id> \
  --packet <review-packet-path-or-id> --approver local-user \
  --reason "Approved after reviewing the bound packet."
```

Run and replay a local Codex process from a versioned request:

```text
agentmem agent run codex --root <evidence> --file <request.json> \
  --codex <codex-executable>
agentmem agent verify --root <evidence>
```

The local dashboard exposes verification summaries only:

```text
agentmem serve dashboard --root <evidence> --repo <private-memory>
```

See [loadouts](docs/loadouts.md), [native execution](docs/native-execution.md),
[longitudinal studies](docs/longitudinal-study.md), and the
[dashboard](docs/dashboard.md).

## Storage boundaries

| Zone | Contains | May enter Git? |
| --- | --- | --- |
| Local evidence | Exact transcripts, tool output, exposed reasoning, normalized events, gaps, receipts | No |
| Portable memory | Attested and redacted Markdown/YAML revisions plus immutable loadouts | Separate private repository only |
| Encrypted backup | Complete local evidence snapshot | Optional backup backend, never the memory repository |

Do not copy `~/.codex`, Agent state databases, session directories, or the local
evidence root into the memory repository. SQLite may be used as a rebuildable
local index, never as Git-merged canonical data.

## What the project does not claim

- It does not capture private chain-of-thought hidden by a provider.
- It does not treat a summary as the missing original reasoning.
- It does not automatically promote model-written claims.
- It does not silently merge semantic conflicts.
- It does not prove that retrieval improves work merely because retrieval ran.
- It does not turn a descriptive longitudinal association into causal or
  independently provider-certified efficacy.
- It does not modify Agent rules or install hooks without explicit approval.

## Repository map

- `cmd/agentmem`: cross-platform CLI
- `adapters`: loss-aware Codex, Claude Code, OpenCode, and DeepSeek Harness importers
- `internal/ledger`: append-only evidence and content-addressed blobs
- `internal/episodes`, `internal/candidates`: reconstruction and extraction
- `internal/review`, `internal/promotion`: attested decisions and memory lifecycle
- `internal/portable`, `internal/gitsync`: portable projection and private Git
- `internal/loadout`, `internal/retrieval`, `internal/mcpserver`: exact loadouts,
  bounded query, and delivery receipts
- `internal/reviewpacket`: immutable candidate review surfaces
- `internal/agentbridge`: replay-verifiable local Codex process evidence
- `internal/study`: prospective assignment, observation, and descriptive reports
- `internal/dashboard`: loopback-only read-only operational summary
- `internal/codexbench`: sealed native Codex baseline/memory diagnostics
- `internal/evaluation`: frozen corpora, paired trials, metrics, and replay
- `internal/backup`, `internal/diagnostics`: encrypted recovery and integrity
- `schemas`: versioned public JSON contracts
- `docs/adr`: architectural decisions and threat boundaries

Detailed protocol documentation lives under [`docs`](docs). Start with the
[product contract](docs/product-contract.md), [threat model](docs/threat-model.md),
and [verification guide](docs/verification.md).

## Development

```text
go test ./...
go vet ./...
go build ./cmd/agentmem
```

The CI matrix runs on Ubuntu, Windows, and macOS. Stateful safety packages also
run with the Go race detector. The OpenCode runtime smoke test installs its
pinned runtime only inside the disposable hosted runner. The DeepSeek Harness
Bundle has its own Node 22.19+/24 build, Cordis composition, and package tests.

See [CONTRIBUTING.md](CONTRIBUTING.md) before changing an adapter, schema, or
evidence boundary. Never attach real transcripts or raw evidence to an issue.

## License

MIT. See [LICENSE](LICENSE).
