# Releasing

Release artifacts are public code and integration assets. Raw evidence,
evaluation review packs, attestations, private memory revisions, local paths,
and backup material never belong in a release.

The repository does not publish a GitHub Release automatically. The
`release-candidate` workflow has read-only repository permission and only
creates a short-lived workflow artifact. Publishing a tag or GitHub Release is
a separate owner action after the candidate has been inspected.

## Preconditions

Before creating a candidate:

1. update `CHANGELOG.md` with the exact SemVer version and date;
2. require all seven checks on the release commit to pass: Ubuntu, Windows,
   macOS, race, fuzz-smoke, OpenCode runtime, and public-tree privacy;
3. require the release commit to be the protected `main` head;
4. run the public-tree checker over the current tree and every reachable ref;
5. verify the separate private memory repository independently and keep every
   raw-evidence location outside both repositories; and
6. keep efficacy claims measurement-only until independently verified native
   Agent execution and the fixed population gates both pass.

## Build a release candidate

Run the manually dispatched `release-candidate` workflow with a SemVer label
such as `v0.1.0-rc.1` from `main`. The workflow refuses a different ref, a
stale `main` commit, or a version absent from `CHANGELOG.md`. It verifies that
all required checks succeeded for the exact commit, scans the reachable public
tree and history, builds Windows, macOS, and Linux archives for amd64 and arm64,
and writes `SHA256SUMS`, `REQUIRED_CHECKS.json`, and `PROVENANCE.json`. The
candidate is uploaded as a workflow artifact with 14-day retention.

The workflow cannot push a tag or create a release.

Each archive contains:

- the platform executable;
- `README.md` and `LICENSE`;
- versioned schemas and documentation;
- installer scripts;
- optional Codex, Claude Code, and OpenCode integration assets; and
- the three adapter capability contracts.

## Approve publication

Before publishing:

1. download the candidate artifact from GitHub Actions;
2. verify `SHA256SUMS` and inspect at least one archive;
3. install the exact candidate on Windows and GitHub-hosted macOS, then run
   `agentmem version` and the quickstart;
4. confirm the `main` head has not changed since the candidate build; and
5. obtain an explicit owner decision to create the tag and GitHub Release.

Only then create the annotated SemVer tag and publish the already inspected
archives. A prerelease tag contains a hyphen, such as `v0.2.0-alpha.1`, and
must be marked as a GitHub prerelease so the `latest` installer path does not
select it.

## Verify the published release

After publication:

1. download `SHA256SUMS` and at least one archive from the public release rather
   than reusing a local file;
2. verify the archive checksum;
3. install on Windows and macOS using the pinned tag and run `agentmem version`;
4. inspect one archive to confirm the documented integration assets are
   present; and
5. run a new-device recovery-integrity check without enabling hooks, plugins,
   or automatic synchronization implicitly.

Publishing a release does not promote memory, install Agent configuration, or
establish real-world learning efficacy.
