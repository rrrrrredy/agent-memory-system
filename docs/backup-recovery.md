# Encrypted evidence backup and recovery

The portable memory Git repository is not a backup of raw evidence. Use this
workflow when complete local task evidence needs a separately encrypted disaster
copy.

## Create recovery material

Generate a dedicated recovery identity outside every Git worktree:

```text
agentmem backup keygen --identity <separate-private-key-file>
```

The command prints the public recipient. Store the private identity separately
from both the evidence device and the backup object. An offline copy or a secret
manager with independent credentials is appropriate. The identity file itself
is secret plaintext and must not be committed or uploaded beside the archive.
Key generation, verification, and restore all refuse identities located inside
a Git worktree or an evidence store.

Create a new archive using the public recipient:

```text
agentmem backup create \
  --root <local-evidence-directory> \
  --output <separate-backup-directory>/evidence-<date>.age \
  --recipient <age-public-recipient>
```

Repeat `--recipient` to allow a second independently held recovery key. Creation
requires a quiescent store and never replaces an existing output. The output is
refused inside any Git worktree, including the private promoted-memory
repository.

Verify the encrypted object with a separately held identity before retaining or
uploading it:

```text
agentmem backup verify \
  --archive <encrypted-archive> \
  --identity <separate-private-key-file>
```

Verification is streaming. It authenticates the age ciphertext, manifest,
every file hash, the evidence record chain, and all referenced blobs without
extracting plaintext to disk. Verification and restore default to a 4 TiB and
10,000,000-file safety ceiling; `--max-bytes` and `--max-files` may raise or
lower those explicit limits for a known corpus.

The resulting `.age` file can be copied to offline media or a cloud backup
backend that is independent of the private memory Git repository. Upload and
retention are deliberately not coupled to Git synchronization.

## Restore after loss

Restore only to a path that does not exist:

```text
agentmem backup restore \
  --archive <encrypted-archive> \
  --identity <separate-private-key-file> \
  --target <new-local-evidence-directory>
```

The command decrypts into a sibling staging directory, verifies the complete
store, appends a local restore record, and then renames it into place. Any
failure removes staging and leaves the target absent. Filesystem deletion is not
secure erasure, so the target volume should provide device encryption.

Restore preserves the logical store identity. Do not continue writing to both
the original and restored copy. For an additional live device, initialize a new
evidence store instead and share only promoted memory through the private Git
repository.

## Install and upgrade

Tagged releases contain checksum-listed binaries for Windows, macOS, and Linux.
From a checked-out release, Windows users can run:

```powershell
.\scripts\install.ps1 -Version <release-tag> -InstallDir <binary-directory>
```

On macOS:

```sh
AGENTMEM_VERSION=<release-tag> AGENTMEM_INSTALL_DIR=<binary-directory> ./scripts/install.sh
```

Both installers download one release archive, verify it against the release
`SHA256SUMS`, replace only the executable, and run `agentmem version`. They do
not create evidence, edit Agent configuration, install hooks, or enable
automatic synchronization. Re-running the same installer with a newer release
is the upgrade path and leaves both storage repositories untouched.

## New-device acceptance

For a replacement device:

1. install and verify the binary;
2. restore the encrypted evidence archive to a new local path;
3. clone the separate private promoted-memory repository;
4. run the complete diagnostic gate;
5. test one offline retrieval for each Agent that will use the device;
6. enable hooks, plugins, MCP configuration, or automatic synchronization only
   after explicit approval.

The diagnostic gate is:

```text
agentmem doctor \
  --root <local-evidence-directory> \
  --repo <private-memory-directory> \
  --require-repo
```

It checks the evidence chain and blobs, candidate reviews, promoted revisions,
rule-change approvals, retrieval and adoption receipts, the complete portable
repository, and every reachable Git data commit. A non-ready report exits with
an error.

For an additional device rather than a replacement, use `agentmem init` for a
new local evidence root, clone and verify the private memory repository, then
import only that device's locally available Agent histories.
