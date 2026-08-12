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
The ordinary lifecycle smoke now uses `onboard`, `status`, current-generation
review, separate synthetic validation and promotion, portable export, and exact
retrieval. The fixture manifest pins the expected candidate text and hashes.
This proves the control path; synthetic attestations are not human decisions.

The native Codex benchmark stores the raw input plan, sealed plan, exact runner,
Codex and oracle executable bytes, workspace and oracle artifacts, raw Codex
JSONL, final agent message, token usage, and terminal receipts in the local
content-addressed evidence store.


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
- a local authenticated 20-pair `codex exec` suite in which every synthetic
  instruction traversed import, derivation, review, promotion, export,
  retrieval, and a verified injection receipt before exact-oracle evaluation;

The Codex acceptance is not independently reproducible from this repository:
its private transcript and correlatable source hash are intentionally not
published. The OpenCode hosted receipt exposes only aggregate counts and hashes
of the matched event and session identifier. No raw transcript, local path,
thread identifier, user instruction, session identifier, or memory content is
committed.

The paired Codex diagnostic has 20 distinct task clusters, 19 wins, 1 tie,
0 losses, baseline 1/20, memory 20/20, and a one-sided sign-test value of
`0.0000019073486328125`. Every arm sealed `tool_policy=forbid` and emitted zero
tool calls. The local verifier replayed 204 ledger records and 229 blobs without
issues. Its exact report and raw events remain local because they contain provider
event identifiers and environment-correlatable metadata. It establishes that
the end-to-end mechanism changes outcomes on the frozen synthetic suite; it
does not establish a general or longitudinal improvement.

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
### Native Codex diagnostic benchmark

`agentmem eval codex benchmark` preregisters a local-only paired suite before
execution. It uses one fresh Git workspace per arm, derives arm order from the
sealed task artifacts, binds exact runner, Codex, and oracle executable bytes,
and records terminal evidence even when an arm fails. Oracle arguments cannot
reference unsealed files. The public suite seals a no-tools policy, rejects any
Codex tool item, and supplies only the expected-answer hash to the oracle.
`eval codex verify` replays the full local evidence graph; `eval codex receipt`
exports only a self-hashed aggregate receipt.

A task may use caller-provided context for debugging, but that source is always
reported as diagnostic. Only an `injection_id` resolved from a fully verified
retrieval and injection receipt graph counts as promoted-memory exposure.
Observed benefit requires at least 20 verified-retrieval pairs from 20 distinct
task clusters, no infrastructure issue, more wins than losses, and a one-sided
paired sign-test value at or below `0.05`. All pairs must also have sealed
no-tools policy and zero tool calls. Even then the claim is restricted to
the exact sealed suite and is not longitudinal certification. The separate
fixed-population continuous-learning gate remains fail-closed until native
Agent execution provenance satisfies its stronger contract.


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
