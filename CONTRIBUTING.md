# Contributing

Thank you for improving Agent Memory System. Changes are welcome when they
preserve the project's evidence, privacy, and fail-closed boundaries.

## Before opening a change

1. Read `docs/product-contract.md` and `docs/threat-model.md`.
2. Check the relevant architecture decision under `docs/adr`.
3. Open an issue for a wire-format change, a new trust assumption, or a change
   that could move raw evidence across a device boundary.

Never attach a real transcript, tool output, reasoning artifact, credential,
personal path, or private memory repository to an issue or pull request. Build
the smallest synthetic fixture that reproduces the behavior.

## Development setup

Requirements:

- Go 1.25 or newer;
- Node.js 24 and pnpm 11 only when changing the OpenCode integration;
- Git for synchronization tests.

Run the main checks from the repository root:

```text
go test ./...
go vet ./...
go build ./cmd/agentmem
```

For OpenCode integration changes:

```text
cd integrations/opencode
pnpm install --frozen-lockfile --ignore-scripts
pnpm test
pnpm typecheck
```

The OpenCode runtime itself is not required locally. A pinned runtime smoke test
runs in a disposable GitHub-hosted Ubuntu runner.

## Change requirements

### Evidence and ledger

- Preserve exact source bytes before projecting normalized events.
- Add explicit gaps for missing or unsupported data.
- Keep writes append-only and safe under process interruption.
- Add negative tests for tampering, duplicate input, and stale state.

### Agent adapters

- Update the adapter's `CAPABILITIES.md`.
- Add fixtures for every new event shape and at least one unknown shape.
- Prove idempotent re-import and loss reporting.
- Do not describe private provider reasoning as captured unless the runtime
  exposes the exact bytes locally.

### Public schemas

- Keep `additionalProperties: false` unless extensibility is intentional.
- Version incompatible wire changes; never reinterpret an existing version.
- Add a Go-producer instance and a rejection case under `schemas`.
- Preserve mixed-version ledger verification when a ledger event version moves.

### Review, promotion, and retrieval

- Model output alone is not sufficient evidence for automatic promotion.
- Keep review and promotion as separate human decisions.
- Do not weaken secret scanning or semantic-conflict rejection.
- Retrieval must fail closed when the portable repository is invalid.

## Pull requests

A pull request should state:

- the user-visible outcome;
- the trust or privacy boundary affected;
- the exact tests run;
- any compatibility evidence that is still missing.

Keep commits focused and use imperative subjects. Do not include local paths,
task identifiers, generated review transcripts, or development scratch files.

## Security reports

Do not open a public issue for a vulnerability that contains sensitive data.
Follow [SECURITY.md](SECURITY.md).
