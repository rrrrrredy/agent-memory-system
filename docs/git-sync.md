# Private Git synchronization

The private data repository contains readable promoted memory only. It is not
an evidence backup. Create the remote as an empty private repository so no
unrelated root commit or hosting template enters its history.

## Bootstrap

Initialize the local data repository, attach its private remote, create the
canonical root commit, and install repository-local guards:

```text
agentmem sync bootstrap \
  --repo <private-memory-directory> \
  --remote-url <private-git-url>
```

The command is idempotent for an already clean repository. It never rewrites
an existing history. If another hook already occupies `pre-commit` or
`pre-push`, bootstrap stops instead of overwriting it; integrate the guard
manually or use `--hooks=false` and install it later.

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
fixed.
