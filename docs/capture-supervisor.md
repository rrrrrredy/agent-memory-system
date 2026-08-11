# Capture supervision

The capture supervisor turns the three Agent adapters into one repeatable local
operation. It does not edit Codex, Claude Code, or OpenCode configuration and it
does not install hooks, plugins, services, or system schedules.

## Configure sources

Create a local JSON file outside every Git worktree. Copying the following
example is safer than placing a machine-specific config in this repository:

```json
{
  "schema_version": "capture-supervisor-config/v1alpha1",
  "interval_seconds": 900,
  "full_reconcile_every_runs": 96,
  "source_timeout_seconds": 1800,
  "sources": [
    {
      "id": "codex",
      "agent": "codex",
      "kind": "codex_rollouts",
      "required": true,
      "path": "<absolute-codex-sessions-path>"
    },
    {
      "id": "claude",
      "agent": "claude_code",
      "kind": "claude_home",
      "required": true,
      "path": "<absolute-claude-home-path>"
    },
    {
      "id": "opencode-native",
      "agent": "opencode",
      "kind": "opencode_native",
      "required": true,
      "binary_path": "<absolute-opencode-executable-path>",
      "staging_root": "<absolute-non-git-staging-path>"
    },
    {
      "id": "opencode-events",
      "agent": "opencode",
      "kind": "opencode_events",
      "required": false,
      "path": "<absolute-opencode-event-spool-path>"
    }
  ],
  "privacy": "local_only"
}
```

Apply it explicitly:

```text
agentmem capture supervisor configure \
  --root <local-evidence-directory> \
  --file <local-config-file-outside-git>
```

All paths must be absolute and separate from the evidence root. Source,
staging, and config-input paths inside a Git worktree or bare repository are
rejected. The normalized config is stored under the local evidence root and is
therefore included in encrypted evidence backup. Config versions are immutable
and activated by a hash-chained `configure` event. The config is the only
supervisor state containing cleartext source paths.

## Run and watch

Run one complete cycle:

```text
agentmem capture supervisor run --root <local-evidence-directory>
```

Or keep a foreground process running at the configured interval:

```text
agentmem capture supervisor watch --root <local-evidence-directory>
```

`watch` starts with an immediate capture. Audited source failures do not stop
later cycles; invalid config, audit tampering, an incomplete prior run, or a
stale operation lock fail closed. `Ctrl+C` records cancellation when the active
adapter can be interrupted and then exits. The supervisor never runs two
sources concurrently because the evidence ledger is single-writer.

This release deliberately provides a foreground watcher, not a system schedule
installer. A future Windows Task Scheduler or macOS launchd integration must
inspect the complete registered command fingerprint instead of treating a task
name as proof that the correct command will run.

## Source inventory

Before a file-backed source is reconciled, the supervisor writes an immutable
inventory containing only path identities hashed with SHA-256, file content
hashes, byte counts, modification times, and safe status codes. It writes a
second inventory after reconciliation and reports failure if the observable
source changed between the two. File-backed adapters also receive the exact
pre-capture content hashes and byte counts and reject any preserved blob that
does not match them. A shared per-run tracker requires every available file in
the pre-capture inventory to be preserved, including sources split across
Claude Code transcript, prompt-history, and companion-file adapters. OpenCode
native capture binds the inventory to an opaque,
persisted session-list artifact before any export begins and revalidates its
store and staging boundaries before use. Cleartext paths and session IDs are
absent from inventory and audit.

Every observed file is content-hashed on every cycle. If a current file is not
an exact append of the previous bytes, that source is fully reconciled even
when its path and size did not change. An additional configured cadence forces
full reconciliation as defense in depth.

An item seen in an earlier inventory but absent after a complete scan remains in
the new inventory as `missing`, retaining its last content metadata, and a
deterministic local evidence gap is recorded. If a permission error, rejected
link, Git boundary, timeout, or incomplete walk prevents a complete scan, prior
items are retained as `unverified` instead of being falsely tombstoned. Any
later reappearance forces full reconciliation. Moving a source does not
silently rebind its logical identity. Each inventory is bound to the hash of its
exact source configuration, so reusing a `source_id` with a new path starts a
new inventory lineage instead of turning the old path into a false gap. Use the
explicit source-recovery manifest when preserving an intentionally moved
logical source identity is required.

Inventory improves loss accounting but does not prove global completeness. A
session that is created and deleted entirely between two scans, never reaches
a hook or event spool, and is not exposed by the Agent cannot be discovered.
Every status therefore carries the non-blocking `coverage_unproven` warning and
uses the phrase `locally_observable_only`.

## Status and diagnostic gates

Inspect strict capture readiness:

```text
agentmem capture supervisor status \
  --root <local-evidence-directory> \
  --require-agent codex \
  --require-agent claude-code \
  --require-agent opencode \
  --max-age 30m
```

A required source is successful only when its inventory is non-empty and
available before and after reconciliation, both observations agree,
reconciliation completes without gaps or issues, and its terminal source event
is committed in the latest run. Recording a missing gap never advances source
freshness. Status exposes `integrity_ready` separately from `capture_ready`.
Audit replay independently requires one terminal result for every configured
source, rejects duplicate terminal results, validates the required pre/post
inventory sequence, and recomputes the run outcome before accepting freshness.
When a strict gate omits `--max-age`, the default age is twice the configured
interval plus one configured source timeout.

The general diagnostic command keeps its original integrity meaning unless a
capture gate is explicitly requested:

```text
agentmem doctor \
  --root <local-evidence-directory> \
  --require-capture-ready \
  --require-capture-agent codex \
  --require-capture-agent claude-code \
  --require-capture-agent opencode \
  --capture-max-age 30m
```

Without these flags, `doctor` still rejects tampered capture configs,
inventories, audit events, and incomplete operations when that state exists,
but it does not require capture supervision to be configured.

## Crash recovery

Each audit event and inventory is committed as an immutable file through a
temporary file, file sync, a no-replace same-filesystem link, and directory sync
where supported.
An operating-system file lock covers the whole capture cycle. The lock file is
a persistent slot and is never deleted by run, recovery, or release, avoiding a
delete-and-reacquire race. PID and age are diagnostic metadata, not proof that
a lock is stale.

After independently confirming that no capture process is active, clear only
the stale lock metadata. The command first acquires the operating-system lock
and therefore refuses an active holder:

```text
agentmem capture supervisor clear-stale-lock \
  --root <local-evidence-directory> \
  --confirm-no-active-run
```

Then record an interrupted cycle explicitly:

```text
agentmem capture supervisor recover --root <local-evidence-directory>
```

Recovery never removes an existing supervisor lock. It records a terminal
`recovery_abandoned` result for every unfinished configured source and then
records the run as `run_abandoned`. An abandoned latest run cannot establish
freshness; a new complete run is required. Recovery does not clear the
evidence-ledger writer lock, delete inventory, guess source relocation, or
modify Agent configuration. Re-running capture then relies on the adapters'
idempotent evidence identities.
