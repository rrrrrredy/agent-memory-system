# Install, upgrade, and uninstall

Agent Memory System is a single CLI binary. The installer does not create an
evidence directory, install Agent hooks, register automatic sync, or delete
existing data.

## Build from source

Go 1.25 or newer is required.

Windows PowerShell:

```powershell
New-Item -ItemType Directory -Force .\bin | Out-Null
go build -o .\bin\agentmem.exe .\cmd\agentmem
.\bin\agentmem.exe version
```

macOS or Linux:

```sh
mkdir -p ./bin
go build -o ./bin/agentmem ./cmd/agentmem
./bin/agentmem version
```

## Install a tagged release

Tagged releases include Windows, macOS, and Linux archives for amd64 and arm64 plus a
`SHA256SUMS` file. The installers download both the archive and checksum file,
verify the selected archive, stage the binary, run `agentmem version`, and only
then replace the installed binary.

From a checked-out release on Windows:

```powershell
.\scripts\install.ps1 -Version v0.3.0
```

Omit `-Version` to install the latest non-prerelease GitHub release. The default
destination is `%LOCALAPPDATA%\Programs\agentmem\agentmem.exe`. Use
`-InstallDir` to choose another directory.

From a checked-out release on macOS:

```sh
AGENTMEM_VERSION=v0.3.0 sh ./scripts/install.sh
```

Omit `AGENTMEM_VERSION` to install the latest non-prerelease release. The
default destination is `${HOME}/.local/bin/agentmem`. Set
`AGENTMEM_INSTALL_DIR` to choose another directory.

Linux users should build from source or verify and unpack the matching release
archive directly; the shell installer intentionally supports macOS only.

## Upgrade

Run the same installer with the target version. The replacement is staged and
verified before it replaces the current binary. Evidence, portable memory,
backup keys, hooks, and scheduler state are not migrated or deleted by an
upgrade.

After upgrading:

```text
agentmem version
agentmem doctor --root <local-evidence-directory>
agentmem portable verify --repo <portable-memory-directory>
```

Review [compatibility](compatibility.md) whenever an Agent runtime changes its
local format.

## Uninstall

1. For every portable repository with automatic sync enabled, inspect and then
   disable its current-user scheduler registration:

   ```text
   agentmem sync auto status --repo <portable-memory-directory>
   agentmem sync auto disable --repo <portable-memory-directory>
   ```

2. Stop any foreground `capture watch` process you started. The binary
   installer itself never creates a capture service.
3. If you explicitly installed repository-local Git hooks with `sync
   install-hooks`, inspect `.git/hooks/pre-commit` and `.git/hooks/pre-push` in
   that portable repository. Remove only files whose contents are the generated
   Agent Memory System guard; the project never overwrites a pre-existing hook.
4. Remove the single installed binary from the installation directory and, if
   you added that directory solely for this tool, remove it from `PATH`.

Uninstall intentionally preserves:

- the local evidence directory;
- the separate portable memory Git repository;
- encrypted evidence backups and age identities;
- Agent-native session history.

Those stores may contain irreplaceable or private data. Verify an encrypted
backup before deleting any of them. There is deliberately no recursive data
deletion command in the installer.
