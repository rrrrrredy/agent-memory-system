# Quickstart

This walkthrough imports a privacy-safe synthetic Codex rollout, derives one
candidate, asks the operator to inspect it, records separate validation and
promotion attestations, exports one memory to a separate local repository, and
proves that retrieval selects that exact memory. The CLI binds the records but
does not authenticate that the supplied reviewer or approver identifier belongs
to a human. It does not install hooks, contact a model provider, or upload raw
evidence.

After the demo succeeds, replace the example source with your existing Codex
sessions directory. Keep the evidence directory outside every Git worktree. The
portable memory directory must be a different physical tree.

## Prerequisites

- Go 1.25 or newer;
- Git;
- `jq` for the POSIX walkthrough.

The repository smoke scripts use `examples/quickstart/manifest.json` to pin the
exact synthetic rollout, candidate ID, candidate text hash, and retrieval query.
They record `synthetic-test-attestation` with the explicit `synthetic_test`
reviewer and approver kind. The stored ledger therefore describes automation
as synthetic rather than as a human decision:

```text
scripts/quickstart-smoke.ps1
scripts/quickstart-smoke.sh
```

## Windows PowerShell

Build the CLI and create isolated demo directories:

```powershell
New-Item -ItemType Directory -Force .\bin | Out-Null
go build -o .\bin\agentmem.exe .\cmd\agentmem

$Run = [guid]::NewGuid().ToString("N")
$Evidence = Join-Path $env:TEMP "agentmem-demo-$Run\evidence"
$Memory = Join-Path $env:TEMP "agentmem-demo-$Run\portable-memory"
$DemoSessions = (Resolve-Path .\examples\quickstart).Path
```

The runtime probe is optional. A failed version probe does not disable offline
history import; inspect `history_import_available` separately:

```powershell
$Compatibility = .\bin\agentmem.exe compatibility --agent codex | ConvertFrom-Json
$Compatibility.agents | Select-Object agent,runtime_status,history_import_available,runtime_issue
```

Import and verify the synthetic rollout:

```powershell
.\bin\agentmem.exe init --root $Evidence
$Import = .\bin\agentmem.exe import codex --root $Evidence --path $DemoSessions | ConvertFrom-Json
$Doctor = .\bin\agentmem.exe doctor --root $Evidence | ConvertFrom-Json
if ($Import.gaps_appended -ne 0 -or -not $Doctor.ready) {
  throw "The demo evidence import did not verify cleanly."
}
```

Derive episodes and review-ready candidates:

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

if ($Queue.candidates.Count -eq 0) {
  throw "No review-ready candidate was derived. Inspect the candidate generation before continuing."
}
$Item = $Queue.candidates[0]
$Basis = @("explicit_remember", "user_correction", "stable_repetition") |
  Where-Object { $Item.candidate.support_types -contains $_ } |
  Select-Object -First 1
if (-not $Basis) {
  throw "The selected candidate has no human-validation basis supported by this walkthrough."
}
$Item.candidate | Select-Object candidate_id,text,support_types
```

Read the candidate and its evidence before running this command. `local-user`
is a caller-supplied attestation label, not an authenticated account. In a real
deployment, policy must require the operator to make this decision rather than
letting the Agent attest its own output:

```powershell
.\bin\agentmem.exe review decide `
  --root $Evidence `
  --candidates $Candidates.generation_path `
  --candidate $Item.candidate.candidate_id `
  --action validate `
  --reviewer local-user `
  --scope project `
  --scope-value example-project `
  --basis $Basis `
  --reason "Reviewed the source evidence and confirmed this project preference."
```

Promotion is a separate caller attestation and rescans the exact text. Inspect
the scan result and scope before recording it:

```powershell
$Promoted = .\bin\agentmem.exe promote candidate `
  --root $Evidence `
  --candidates $Candidates.generation_path `
  --candidate $Item.candidate.candidate_id `
  --approver local-user `
  --confirm-text-sha256 $Item.text_sha256 `
  --reason "Approved for portable project memory." | ConvertFrom-Json
```

Export to a separate readable repository, then retrieve using the exact approved
text and require the promoted memory to be selected:

```powershell
.\bin\agentmem.exe portable init --repo $Memory
.\bin\agentmem.exe portable export --root $Evidence --repo $Memory
.\bin\agentmem.exe portable verify --repo $Memory

$Recall = .\bin\agentmem.exe recall search `
  --root $Evidence `
  --repo $Memory `
  --agent codex `
  --scope-project example-project `
  --query $Item.candidate.text | ConvertFrom-Json
if ($Recall.selected.memory_id -notcontains $Promoted.revision.memory_id) {
  throw "Retrieval did not select the promoted memory."
}
$Recall.selected | Select-Object memory_id,text,score,matched_terms
```

Connect `$Memory` to an empty private Git remote only after reviewing the
readable diff. See [git-sync.md](git-sync.md).

## macOS or Linux

Build the CLI and create isolated demo directories:

```sh
mkdir -p ./bin
go build -o ./bin/agentmem ./cmd/agentmem

run_root="$(mktemp -d "${TMPDIR:-/tmp}/agentmem-demo.XXXXXX")"
evidence="$run_root/evidence"
memory="$run_root/portable-memory"
demo_sessions="$(pwd)/examples/quickstart"
```

Probe the runtime without using it as an import gate, then import and verify:

```sh
./bin/agentmem compatibility --agent codex
./bin/agentmem init --root "$evidence"
imported="$(./bin/agentmem import codex --root "$evidence" --path "$demo_sessions")"
doctor="$(./bin/agentmem doctor --root "$evidence")"
printf '%s\n' "$imported" | jq -e '.gaps_appended == 0' >/dev/null
printf '%s\n' "$doctor" | jq -e '.ready == true' >/dev/null
```

Derive and select a review-ready candidate:

```sh
episodes="$(./bin/agentmem derive episodes --root "$evidence")"
episode_path="$(printf '%s\n' "$episodes" | jq -r '.generation_path')"
candidates="$(./bin/agentmem derive candidates --root "$evidence" --episodes "$episode_path")"
candidate_path="$(printf '%s\n' "$candidates" | jq -r '.generation_path')"
queue="$(./bin/agentmem review list --root "$evidence" --candidates "$candidate_path" --status review_ready --limit 20)"
item="$(printf '%s\n' "$queue" | jq -ce '.candidates[0] // error("no review-ready candidate")')"
candidate_id="$(printf '%s\n' "$item" | jq -r '.candidate.candidate_id')"
text_sha256="$(printf '%s\n' "$item" | jq -r '.text_sha256')"
query="$(printf '%s\n' "$item" | jq -r '.candidate.text')"
basis="$(printf '%s\n' "$item" | jq -r '[.candidate.support_types[] | select(. == "explicit_remember" or . == "user_correction" or . == "stable_repetition")] | first // empty')"
test -n "$basis" || { echo "candidate has no supported validation basis" >&2; exit 1; }
printf '%s\n' "$item" | jq '{candidate_id:.candidate.candidate_id,text:.candidate.text,support_types:.candidate.support_types}'
```

Read the text and evidence first. The following reviewer and approver strings
are caller attestations, not authenticated human identities:

```sh
./bin/agentmem review decide \
  --root "$evidence" \
  --candidates "$candidate_path" \
  --candidate "$candidate_id" \
  --action validate \
  --reviewer local-user \
  --scope project \
  --scope-value example-project \
  --basis "$basis" \
  --reason "Reviewed the source evidence and confirmed this project preference."

promoted="$(./bin/agentmem promote candidate \
  --root "$evidence" \
  --candidates "$candidate_path" \
  --candidate "$candidate_id" \
  --approver local-user \
  --confirm-text-sha256 "$text_sha256" \
  --reason "Approved for portable project memory.")"
memory_id="$(printf '%s\n' "$promoted" | jq -r '.revision.memory_id')"

./bin/agentmem portable init --repo "$memory"
./bin/agentmem portable export --root "$evidence" --repo "$memory"
./bin/agentmem portable verify --repo "$memory"
recall="$(./bin/agentmem recall search \
  --root "$evidence" \
  --repo "$memory" \
  --agent codex \
  --scope-project example-project \
  --query "$query")"
printf '%s\n' "$recall" | jq -e --arg id "$memory_id" '.selected | any(.memory_id == $id)' >/dev/null
printf '%s\n' "$recall" | jq '.selected'
```

## Use existing Codex history

After the synthetic demo succeeds, reuse the same flow with a new evidence
directory and your actual sessions path:

- Windows: `${CODEX_HOME:-$HOME\.codex}\sessions` (PowerShell should resolve
  `$env:CODEX_HOME` explicitly as shown below);
- macOS/Linux: `${CODEX_HOME:-$HOME/.codex}/sessions`.

PowerShell path selection:

```powershell
$CodexRoot = if ($env:CODEX_HOME) { $env:CODEX_HOME } else { Join-Path $HOME ".codex" }
$Sessions = Join-Path $CodexRoot "sessions"
```

Real history may legitimately produce zero review-ready candidates. That is a
safe result, not a reason to weaken the evidence policy or manufacture a memory.
If evidence changes after derivation, rerun `derive episodes` and `derive
candidates` before review or promotion.

## JSON contracts

Every versioned JSON result used in this walkthrough has a matching public
schema under `schemas/`, including history import, episode and candidate builds,
review and promotion application, portable initialization, export, verification,
and retrieval. The schema tests validate real Go result types and reject an
unknown protocol version. Plain path output from `init` is intentionally not a
versioned JSON protocol.

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

If doctor reports `writer_lock_present`, do not remove it until you have
confirmed no writer is active. Only then use `doctor --clear-stale-writer-lock`.

For encrypted evidence backup and no-overwrite restore, see
[backup-recovery.md](backup-recovery.md).
