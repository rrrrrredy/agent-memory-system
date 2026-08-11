# Verification model

The project separates control correctness, runtime compatibility, and learning
efficacy. A green result in one category is not evidence for another.

## 1. Control correctness

Unit, integration, schema, and cross-platform tests verify invariants such as:

- exact-byte import and idempotent re-import;
- hash-chain, blob, and source-snapshot integrity;
- cross-process writer exclusion and explicit stale-lock recovery;
- crash-safe OpenCode spooling and partial-tail preservation;
- episode and candidate provenance;
- conflict-group completeness and human-bound review transitions;
- promotion, supersession, revocation, and secret redaction;
- immutable portable revisions and semantic-conflict rejection;
- Git divergence, offline operation, and recovery;
- retrieval scope, budgets, exact injection receipts, and adoption evidence;
- encrypted backup verification and no-overwrite restore;
- mixed evidence-event protocol versions and public-schema conformance.

Stateful safety packages also run under the Go race detector.

## 2. Runtime compatibility

Runtime evidence must involve the actual executable, not only a fixture.

The current acceptance includes:

- a maintainer-reported private Codex rollout imported on Windows: 56.9 MB,
  29,577 normalized events,
  zero importer gaps, idempotent re-import, verified episode derivation, and a
  16-item review-ready queue from 84 candidates;
- a maintainer-reported minimal authenticated `codex exec --ephemeral --json`
  run that completed
  successfully without tool calls;
- a pinned OpenCode server and plugin running in a disposable GitHub-hosted
  Ubuntu runner, producing a `session.created` event whose session identifier
  matches the session returned by the server and is imported and verified;
- GitHub-hosted Windows, macOS, and Ubuntu build, test, installer, and protocol
  coverage.

The Codex acceptance is not independently reproducible from this repository:
its private transcript and correlatable source hash are intentionally not
published. The OpenCode hosted receipt exposes only aggregate counts and hashes
of the matched event and session identifier. No raw transcript, local path,
thread identifier, user instruction, session identifier, or memory content is
committed.

An authenticated Claude Code task and a physical macOS device test are not
claimed.

## 3. Learning efficacy

The evaluator measures:

- capture coverage;
- incorrect-memory rate;
- repeated user corrections;
- compaction goal drift;
- paired task outcome change;
- retrieval token cost;
- cross-Agent strata and frozen-corpus coverage.

Trials are preregistered as paired baseline and memory arms. Corpora, system
artifacts, oracle definitions, execution order, compaction labels, and source
inventories are hash-bound to the evidence prefix. Missing denominators,
unpaired attempts, failed execution, late records, or mutable dependencies make
the result not evaluable.

The current local execution supervisor can verify a pinned adapter process but
cannot independently certify that Codex, Claude Code, or OpenCode produced the
result. The fixed population builder therefore sets
`verified_agent_execution=false`, and the continuous efficacy report cannot
become release-ready. This is an intentional truth boundary, not a passing
placeholder.

## Reproduce public checks

```text
go test ./...
go vet ./...
go build ./cmd/agentmem
```

OpenCode integration contracts:

```text
cd integrations/opencode
pnpm install --frozen-lockfile --ignore-scripts
pnpm test
pnpm typecheck
```

The actual OpenCode runtime smoke is CI-only by design. It installs the pinned
runtime into the hosted runner's temporary directory and uses no provider key.

Run non-mutating local checks with:

```text
agentmem compatibility
agentmem doctor --root <local-evidence-directory>
agentmem portable verify --repo <portable-memory-directory>
agentmem sync verify --repo <portable-memory-directory>
```

## Claim language

Use these terms precisely:

- **fixture-tested**: a frozen input exercises a parser or control;
- **runtime-smoked**: the actual executable and integration ran;
- **provider-backed**: an authenticated model task ran;
- **release-ready efficacy**: every fixed population gate passed against
  independently verified Agent execution.

Never replace one term with a stronger one because the test suite is green.
