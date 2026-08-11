# Private Git synchronization

The private data repository contains readable promoted memory only. It is not
an evidence backup. Create the remote as an empty private repository so no
unrelated root commit or hosting template enters its history.

## Bootstrap

Initialize the local data repository, attach its private remote, and create the
canonical root commit:

```text
agentmem sync bootstrap \
  --repo <private-memory-directory> \
  --remote-url <private-git-url>
```

The command is idempotent for an already clean repository and never rewrites an
existing history. Repository-local guards are opt-in:

```text
agentmem sync install-hooks --repo <private-memory-directory>
```

If another hook already occupies `pre-commit` or `pre-push`, installation stops
instead of overwriting it. `sync bootstrap --hooks=true` is an equivalent
explicit opt-in during initialization.

Use a credential manager or SSH remote. Do not embed access tokens in the
remote URL. The synchronized branch is fixed as `main` so branch names cannot
become an alternate metadata channel.

## Manual synchronization

Export reviewed local memory, then invoke synchronization explicitly:

```text
agentmem portable export \
  --root <local-evidence-directory> \
  --repo <private-memory-directory>

agentmem sync run --repo <private-memory-directory>
```

The operation:

1. verifies the current portable tree and existing local history;
2. stages only allowlisted data and creates an append-only local commit;
3. fetches the named remote branch into an isolated temporary reference;
4. verifies every reachable remote commit, including files later deleted;
5. fast-forwards, pushes, or tests a divergent merge in a temporary worktree;
6. advances and pushes only a conflict-free, fully verified result.

Run a read-only audit at any time:

```text
agentmem sync verify --repo <private-memory-directory>
```

Verification rejects modified or deleted immutable paths, executable blobs,
unknown tracked files, force-added `.agentmem` state, noncanonical revisions,
secret findings, revision forks, and semantic conflicts. Unknown filenames are
reported by SHA-256 rather than echoed.

## Optional automatic synchronization

Automatic synchronization is disabled by default and must be enabled on each
device. Use a stable installed `agentmem` executable rather than a temporary
build path:

```text
agentmem sync auto enable \
  --root <local-evidence-directory> \
  --repo <private-memory-directory> \
  --interval 15m
```

Windows registers a current-user Task Scheduler job. macOS installs a
current-user launchd agent. Each finite scheduled invocation:

1. exports only currently eligible promoted memory from the local ledger;
2. invokes the same verified Git synchronization path in non-interactive mode;
3. records the export summary, complete Git result, and local error detail in a
   hash-chained attempt event under `.agentmem`;
4. retries transient failures with bounded exponential delay;
5. suspends on conflict, rejected history, or retry exhaustion.

Inspect, invoke, recover, or disable the local schedule with:

```text
agentmem sync auto status --repo <private-memory-directory>
agentmem sync auto run --repo <private-memory-directory>
agentmem sync auto recover --repo <private-memory-directory>
agentmem sync auto disable --repo <private-memory-directory>
```

Recovery clears retry or suspension state but does not resolve the underlying
Git, authentication, evidence, or conflict fault. Fix and verify that fault
first. If a process terminated while holding the local automation lock, verify
that no `agentmem` process is active before using `sync auto recover
--clear-stale-lock`.

Local automation config contains device paths and never enters Git. The
operating-system registration contains only the executable path and portable
repository path; it contains no remote URL, credential, evidence path, or
memory content.

## New device

On Windows, enable Git long-path support during clone, then install the local
guards and verify before use:

```text
git -c core.longpaths=true clone <private-git-url> <private-memory-directory>
agentmem sync install-hooks --repo <private-memory-directory>
agentmem sync verify --repo <private-memory-directory>
```

The clone remains usable offline. A failed fetch, rejected history, or merge
conflict leaves the current local branch intact. Retrying `sync run` is safe
after authentication, connectivity, or an explicitly resolved conflict is
fixed. Automatic synchronization remains disabled on the new device until it
is explicitly enabled with that device's local evidence path.
