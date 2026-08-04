# ADR 0008: Optional automatic Git synchronization

- Status: accepted
- Date: 2026-08-04

## Decision

Manual synchronization remains the default. Automatic synchronization is an
explicit, per-device option implemented as a finite scheduled CLI invocation,
not a resident daemon.

`sync auto enable` requires both the local evidence ledger and the separate
portable memory repository. Each scheduled attempt first projects currently
eligible promoted memory through `portable export`, then calls the same Git
synchronization operation used by `sync run` with terminal prompts disabled.
It cannot bypass promotion, portable projection, history verification, secret
scanning, merge quarantine, or the repository operation lock.

Windows uses a current-user Task Scheduler registration. macOS uses a
current-user launchd agent with `ProgramArguments` and `StartInterval`. These
are the native periodic scheduling facilities documented by
[Microsoft](https://learn.microsoft.com/en-us/windows-server/administration/windows-commands/schtasks)
and
[Apple](https://developer.apple.com/library/archive/documentation/MacOSX/Conceptual/BPSystemStartup/Chapters/CreatingLaunchdJobs.html).
The registered command contains the installed executable path and portable
repository path, but no remote URL, credential, evidence path, or memory text.

All automation configuration and observability remain under the portable
repository's ignored `.agentmem/automatic-sync` directory. The mutable local
config is bound to an append-only, hash-chained event log. Status compares the
config, audit state, and operating-system registration and reports mismatches.
Each attempt event retains the local export summary, complete synchronization
result, and locally available error detail. Status exposes only a safe summary.
No automation state enters the private Git repository.

Retry delay doubles from the configured schedule interval up to a configured
maximum. A semantic conflict or rejected remote history suspends immediately.
Other failures suspend after the configured consecutive-error limit. Recovery
is explicit and does not claim that the underlying fault was fixed; it only
permits a new verified attempt. Stale operation locks require a separate,
explicit recovery flag after the operator verifies that no run is active.

## Alternatives

- A resident cross-platform daemon was rejected because it adds a second
  lifecycle, privilege, upgrade, and crash-recovery surface.
- Git hooks were rejected as the automation mechanism because they are
  event-bound, can block unrelated Git actions, and do not provide periodic
  offline recovery.
- Agent-specific schedulers and hooks were rejected because they would make
  synchronization depend on Codex, Claude Code, or OpenCode being active.
- Directly copying local evidence or native Agent state was rejected because
  it violates the portable-memory trust boundary.

## Consequences

- Enabling automation is a visible operating-system mutation and is never
  performed by bootstrap or new-device restore.
- Moving the repository or installed executable requires disable and re-enable.
- A missing scheduler entry, altered config, broken audit chain, conflict,
  retry exhaustion, or orphaned schedule remains visible through status.
- An existing schedule without matching local config is never overwritten;
  it must be removed through the explicit disable path first.
- Offline failures retain the local clone and enter retry state without
  weakening validation.
