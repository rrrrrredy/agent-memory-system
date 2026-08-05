# Releasing

Release artifacts are public code and integration assets. Raw evidence,
evaluation review packs, attestations, private memory revisions, local paths,
and backup material never belong in a release.

## Preconditions

Before creating a tag:

1. confirm the release commit is reachable from `main` and the worktree is
   clean;
2. require the Ubuntu, Windows, and macOS CI jobs to pass for that commit;
3. inspect the complete public Git diff and reachable history for credentials,
   raw task evidence, and machine-specific paths;
4. run `go test ./...`, `go vet ./...`, the OpenCode tests and typecheck, and
   the platform installer tests;
5. verify the separate private memory repository independently and keep every
   raw-evidence location outside both repositories;
6. do not make a continuous-learning efficacy claim in the current version.
   Evidence-bound runs remain measurement-only until fixed versioned policy,
   deterministic complete-population reconstruction, and independently
   runnable oracles are implemented together. Synthetic fixtures prove metric
   and gate behavior only.

The release workflow accepts SemVer tags without build metadata, such as
`v0.1.0` or `v0.2.0-alpha.1`, and rejects a tag whose commit is not reachable
from `origin/main`. A prerelease tag is published as a GitHub prerelease and is
not selected by installers that request the latest stable release.

## Publish

Create and push the approved tag only after the preconditions pass. The GitHub
workflow reruns the test suite, builds Windows, macOS, and Linux archives for
amd64 and arm64, and publishes `SHA256SUMS` with the archives.

Each archive contains:

- the platform executable;
- `README.md` and `LICENSE`;
- versioned schemas and documentation;
- the installer scripts;
- optional Codex, Claude Code, and OpenCode integration assets; and
- the three adapter capability contracts.

## Verify the published release

After publication:

1. download `SHA256SUMS` and at least one archive from GitHub rather than using
   a local build;
2. verify the archive checksum;
3. install on Windows and macOS using the pinned tag and run `agentmem version`;
4. inspect one archive to confirm the documented integration assets are
   present; and
5. run a new-device recovery-integrity check without enabling hooks, plugins,
   or automatic synchronization implicitly.

Publishing a release does not promote any memory, install Agent configuration,
or establish real-world learning efficacy.
