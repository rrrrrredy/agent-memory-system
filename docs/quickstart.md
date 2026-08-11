# Quickstart

This path imports existing Codex history, derives candidates, requires a human
decision, exports one approved memory to a separate local repository, and
retrieves it. It does not install hooks, upload raw evidence, or automatically
approve a candidate.

Keep the evidence directory outside every Git worktree. The portable memory
directory must be a different physical tree.

## Build

```text
git clone https://github.com/rrrrrredy/agent-memory-system.git
cd agent-memory-system
go build -o agentmem ./cmd/agentmem
```

On Windows, use `agentmem.exe` in the commands below.

## Windows PowerShell

Build and choose private local directories:

```powershell
New-Item -ItemType Directory -Force .\bin | Out-Null
go build -o .\bin\agentmem.exe .\cmd\agentmem

$Evidence = Join-Path $env:LOCALAPPDATA "agentmem\evidence"
$Memory = Join-Path $env:LOCALAPPDATA "agentmem\portable-memory"
$CodexRoot = if ($env:CODEX_HOME) { $env:CODEX_HOME } else { Join-Path $HOME ".codex" }
$Sessions = Join-Path $CodexRoot "sessions"
```

Check the local executable and import existing rollouts:

```powershell
.\bin\agentmem.exe compatibility --agent codex
.\bin\agentmem.exe init --root $Evidence
.\bin\agentmem.exe import codex --root $Evidence --path $Sessions
.\bin\agentmem.exe doctor --root $Evidence
```

Derive episodes and candidates from the verified evidence prefix:

```powershell
$Episodes = .\bin\agentmem.exe derive episodes --root $Evidence | ConvertFrom-Json
$Candidates = .\bin\agentmem.exe derive candidates `
  --root $Evidence `
  --episodes $Episodes.generation_path | ConvertFrom-Json
$Queue = .\bin\agentmem.exe review list `
  --root $Evidence `
  --candidates $Candidates.generation_path `
  --status review_ready `
  --limit 20 | ConvertFrom-Json

$Queue.candidates | ForEach-Object {
  [pscustomobject]@{
    id = $_.candidate.candidate_id
    text = $_.candidate.text
    support = ($_.candidate.support_types -join ",")
    text_sha256 = $_.text_sha256
  }
}
```

Inspect the text and its evidence basis. Select one item only if it states a
durable instruction you want future Agents to receive:

```powershell
$Item = $Queue.candidates[0]
.\bin\agentmem.exe review decide `
  --root $Evidence `
  --candidates $Candidates.generation_path `
  --candidate $Item.candidate.candidate_id `
  --action validate `
  --reviewer local-user `
  --scope project `
  --scope-value example-project `
  --basis explicit_remember `
  --reason "Reviewed the source evidence and confirmed this project preference."
```

Use `explicit_remember` only when it appears in `support_types`. Other accepted
bases are `user_correction`, `stable_repetition`, `outcome_evidence`, and
`explicit_user_confirmation`. The last two also require explicit evidence event
IDs. Conflicted candidates must be handled with an atomic review request.

Promotion is a second human gate. It rescans the exact text and requires the
confirmation hash returned by `review list`:

```powershell
.\bin\agentmem.exe promote candidate `
  --root $Evidence `
  --candidates $Candidates.generation_path `
  --candidate $Item.candidate.candidate_id `
  --approver local-user `
  --confirm-text-sha256 $Item.text_sha256 `
  --reason "Approved for portable project memory."
```

Create and verify a separate readable memory repository:

```powershell
.\bin\agentmem.exe portable init --repo $Memory
.\bin\agentmem.exe portable export --root $Evidence --repo $Memory
.\bin\agentmem.exe portable verify --repo $Memory
.\bin\agentmem.exe recall search `
  --root $Evidence `
  --repo $Memory `
  --agent codex `
  --scope-project example-project `
  --query "project preference"
```

Connect `$Memory` to an empty private Git remote only after reviewing the
readable diff. See [git-sync.md](git-sync.md).

## macOS or Linux

```sh
mkdir -p ./bin
go build -o ./bin/agentmem ./cmd/agentmem

evidence="${XDG_DATA_HOME:-$HOME/.local/share}/agentmem/evidence"
memory="${XDG_DATA_HOME:-$HOME/.local/share}/agentmem/portable-memory"
codex_root="${CODEX_HOME:-$HOME/.codex}"
sessions="$codex_root/sessions"

./bin/agentmem compatibility --agent codex
./bin/agentmem init --root "$evidence"
./bin/agentmem import codex --root "$evidence" --path "$sessions"
./bin/agentmem doctor --root "$evidence"

./bin/agentmem derive episodes --root "$evidence"
```

Copy `generation_path` from the episode result:

```sh
./bin/agentmem derive candidates \
  --root "$evidence" \
  --episodes '<episode-generation-path>'

./bin/agentmem review list \
  --root "$evidence" \
  --candidates '<candidate-generation-path>' \
  --status review_ready \
  --limit 20
```

Review, promotion, export, and retrieval use the same arguments shown in the
PowerShell path. JSON output is stable and can be inspected with `jq`.

## Claude Code and OpenCode sources

The same evidence store may import other runtimes:

```text
agentmem import claude-home --root <evidence> --path <claude-home>
agentmem import opencode-export --root <evidence> --path <export-file-or-directory>
agentmem import opencode-events --root <evidence> --path <plugin-spool-directory>
```

OpenCode event capture uses the opt-in plugin under `integrations/opencode`.
Keep its JSONL spool outside every Git worktree. Installing the OpenCode runtime
is not required to use Codex or Claude Code support.

## Recovery and verification

Run integrity checks after capture, before promotion, and after sync:

```text
agentmem doctor --root <evidence>
agentmem review verify --root <evidence>
agentmem promote verify --root <evidence>
agentmem portable verify --repo <portable-memory>
agentmem sync verify --repo <portable-memory>
agentmem recall verify --root <evidence>
```

For encrypted evidence backup and no-overwrite restore, see
[backup-recovery.md](backup-recovery.md).
