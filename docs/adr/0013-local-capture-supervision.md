# ADR 0013: Local capture supervision before system scheduling

- Status: accepted
- Date: 2026-08-05

## Context

Each Agent adapter can reconcile one source, but periodic manual commands do
not expose when a configured source disappeared, was overwritten in place, or
stopped being captured. Installing a system schedule before that state is
auditable would only run an incomplete workflow more often.

The existing automatic Git synchronization scheduler is not generic. Its
registered command is synchronization-specific and its existence check does
not verify the complete task or launchd registration. Reusing it directly
would permit a same-name task with a different command to appear healthy.

## Decision

Add an independent local capture supervisor with explicit `configure`, `run`,
`watch`, `status`, `clear-stale-lock`, and `recover` operations. The initial
watcher is a foreground process. It never edits Agent configuration and does
not install a Windows or macOS schedule.

Store config versions, source inventories, and audit events beneath the local
evidence root so encrypted evidence backup includes them. Config versions are
content-addressed and contain the absolute local paths. Audit and inventory use
only hashes and safe result codes. Each audit record and inventory is an
immutable no-replace file committed from a synced temporary file; a single
append-only JSONL state file is not used because a forced termination can leave
a torn final line.

For file-backed sources, hash every currently discovered file on every cycle.
Compare the prior content hash with the same-length prefix of the current file:

- an exact prefix permits incremental reconciliation;
- replacement, truncation, reappearance, missing sources, unreadable sources,
  or a scheduled full-reconcile cycle requires complete reconciliation;
- a complete scan records a formerly observed absent source as `missing`, while
  an incomplete scan carries it as `unverified` with its last content metadata;
- matching pre-capture and post-capture inventories are required for success;
- adapters must verify preserved blob hashes and byte counts against the
  pre-capture inventory, rather than relying on path observations alone;
- a shared consumption tracker must account for every available pre-capture
  file even when one Agent source is partitioned across multiple adapters;
- every inventory is bound to the exact source-configuration hash, so a reused
  source ID cannot inherit a different path's inventory lineage.

OpenCode native capture records the SHA-256 identities of sessions exposed by
the native session listing. Source paths, native session IDs, command stderr,
and raw exports remain in local config or raw staging, not in audit records.

The supervisor operating-system file lock spans one full run. Its persistent
slot is never removed, which avoids an ABA race between stale-lock cleanup and
a new holder. Adapter ledger locks remain shorter and source-specific. A stale
record or unterminated run is never guessed from process age. Metadata clearing
requires a separate explicit confirmation and first obtains the OS lock;
recovery records unfinished sources before marking a run abandoned.

Replay does not trust a recorded success label. It requires the configured
source set to have exactly one terminal result each, validates the inventory
sequence for every successful result, and recomputes the aggregate run outcome.

## Readiness boundary

Default `doctor` retains its integrity meaning. Capture freshness and required
Agent success become hard gates only through explicit capture requirement
flags. Status reports integrity and operational capture readiness separately.
Presence of a hook envelope, a ledger timestamp, a system task name, or a
recorded gap is not a successful Agent capture.

Every status states that coverage is limited to locally observable sources. No
inventory can detect a session that appears and disappears entirely between
scans without leaving any locally exposed trace.

## Consequences

- The same-size replacement and previously observed deletion cases become
  detectable without claiming access to provider-private data.
- Source failure does not prevent other configured Agents from being attempted.
- Local state is more verbose than one mutable status file, but every decision
  is recoverable and tampering is observable.
- A future system scheduler must be a separate change. It must share a generic
  scheduling layer, verify the full registration fingerprint on Windows and
  macOS, and keep source paths out of the scheduled command.
