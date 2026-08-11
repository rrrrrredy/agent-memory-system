# ADR 0007: Verified manual Git synchronization

- Status: accepted
- Date: 2026-08-04

## Decision

Manual synchronization is an explicit CLI operation and is the default. It
uses Git as an audited transport for portable memory revisions, not as an
unvalidated file copier.

Before network access, synchronization verifies the current portable tree,
the Git index, and every commit reachable from the local branch. Every commit
tree must contain only canonical portable data files. A child commit must
retain every path and byte-identical object from each parent. This detects an
excluded file even if a later commit deletes it.

Commit messages are fixed by parent count: repository initialization, a
single-parent memory update, or a two-parent verified merge. Arbitrary commit
messages and octopus merges are rejected so task text or credentials cannot
bypass the portable projection through Git metadata.

The remote branch is fetched into a temporary local reference. Its complete
reachable history is checked before it can affect the working branch. A
fast-forward is accepted only after validation. Divergent branches are merged
in a detached temporary worktree with Git hooks disabled, then checked for
textual conflicts, revision forks, secret findings, duplicate memories, and
semantic conflicts. The primary branch advances only after the merged tree and
history pass. Pushes are ordinary fast-forward pushes; force push and
last-write-wins are not used.

Export and synchronization share one repository operation lock. The bootstrap
command configures long-path support locally for Windows. Repository-local
pre-commit and pre-push guards require an explicit install command or bootstrap
flag. The sync implementation performs the same checks itself and bypasses
hooks during its own already-verified commits and pushes.

## Consequences

- Local work remains usable when fetching or authentication fails.
- A rejected remote, textual conflict, revision fork, or semantic conflict
  cannot move the local branch or remote branch silently.
- Unknown path names are represented by a digest in verification output so a
  sensitive filename is not echoed into logs.
- Complete history verification is deliberately conservative. Reusable local
  verification checkpoints may optimize it later, but cannot replace checking
  previously untrusted commits.
- Optional automatic synchronization must call this same operation in
  non-interactive mode and add scheduling, retry, and observability only.
