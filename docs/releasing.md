# Releasing

Release artifacts are public code and integration assets. Raw evidence,
evaluation review packs, attestations, private memory revisions, local paths,
and backup material never belong in a release.

The repository does not publish a GitHub Release automatically. The
`release-build` workflow has read-only repository permission and only
creates a short-lived workflow artifact. Publishing a tag or GitHub Release is
a separate owner action after the candidate has been inspected.

## Preconditions

Before building release artifacts:

1. update `CHANGELOG.md` with the exact SemVer version and date;
2. require all seven checks on the release commit to pass: Ubuntu, Windows,
   macOS, race, fuzz-smoke, OpenCode runtime, and public-tree privacy;
3. require the release commit to be the protected `main` head;
4. require every pull request to scan protected `main`, tags, and its own
   complete reachable history; scan protected `main` and tags again when the
   release artifact is built;
   the repository owner's intentionally public commit email is declared in the
   exact public-tree allowlist rather than treated as private data;
5. verify the separate private memory repository independently and keep every
   raw-evidence location outside both repositories; and
6. keep efficacy claims measurement-only until independently verified native
   Agent execution and the fixed population gates both pass.

## Build release artifacts

Run the manually dispatched `release-build` workflow with the exact SemVer label
from the first versioned `CHANGELOG.md` entry, such as `v0.3.0`, on `main`.
The workflow refuses a different ref, a stale `main` commit, or any historical
version other than that current entry. It verifies that
one successful `ci.yml` push run on protected `main` contains every required job,
scans protected `main` and tag history, and builds Windows, macOS, and Linux
archives for amd64 and arm64. Separate GitHub-hosted Windows, macOS, and Linux
jobs then download the exact staging artifact, verify its checksum, execute the
embedded version metadata, and run the full quickstart with the archived
binary. The build job writes the staging `SHA256SUMS`; only after all three
acceptance jobs pass does the workflow assemble the final 14-day candidate with
that checksum file, `REQUIRED_CHECKS.json`, hosted acceptance receipts, and
`PROVENANCE.json`.

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
3. inspect the hosted Windows, macOS, and Linux acceptance receipts and confirm
   all three bind the exact archive checksum, `agentmem version`, and quickstart result;
4. confirm the `main` head has not changed since the candidate build; and
5. obtain an explicit owner decision to create the tag and GitHub Release.

Only then create the annotated SemVer tag and publish the already inspected
archives. A prerelease tag contains a hyphen, such as `v0.3.0-alpha.1`, and
must be marked as a GitHub prerelease so the `latest` installer path does not
select it.

## Verify the published release

After publication:

1. download `SHA256SUMS` and at least one archive from the public release rather
   than reusing a local file;
2. verify the archive checksum;
3. install on Windows and macOS using the pinned tag, execute the matching Linux
   archive, and run `agentmem version` on each platform;
4. inspect one archive to confirm the documented integration assets are
   present; and
5. run a new-device recovery-integrity check without enabling hooks, plugins,
   or automatic synchronization implicitly.

Publishing a release does not promote memory, install Agent configuration, or
establish real-world learning efficacy.
