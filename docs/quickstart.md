# Quickstart

This walkthrough proves the complete user path with a privacy-safe synthetic
Codex rollout. It imports locally available history, verifies the evidence
ledger, derives the current episode and candidate generations, asks the
operator to review one candidate, records a separate promotion decision,
exports only the promoted memory, and retrieves that exact revision.

`onboard` does not approve or promote anything. Reviewer and approver names are
caller-supplied attestations; the CLI binds them to evidence but does not
authenticate a human identity. Raw evidence remains outside Git.

## Prerequisites

- Go 1.25 or newer;
- Git;
- `jq` for the POSIX commands.

Build from the repository root:

```powershell
New-Item -ItemType Directory -Force .\bin | Out-Null
go build -o .\bin\agentmem.exe .\cmd\agentmem
```

```sh
mkdir -p ./bin
go build -o ./bin/agentmem ./cmd/agentmem
```

The automated smoke tests use the same public fixture and record explicit
`synthetic_test` attestations. They never label automation as a human decision:

```text
scripts/quickstart-smoke.ps1
scripts/quickstart-smoke.sh
```

## Windows PowerShell

Create separate local evidence and portable-memory directories:

```powershell
$Run = [guid]::NewGuid().ToString("N")
$Evidence = Join-Path $env:TEMP "agentmem-demo-$Run\evidence"
$Memory = Join-Path $env:TEMP "agentmem-demo-$Run\portable-memory"
$DemoSessions = (Resolve-Path .\examples\quickstart).Path
```

The compatibility probe is optional. `history_import_available` remains
separate from executable runtime status:

```powershell
.\bin\agentmem.exe compatibility --agent codex
```

Import, derive, and verify in one command, then inspect the current state:

```powershell
$Onboard = .\bin\agentmem.exe onboard codex `
  --root $Evidence `
  --path $DemoSessions | ConvertFrom-Json
if (-not $Onboard.ready -or $Onboard.import.gaps_appended -ne 0) {
  throw "Onboarding found evidence gaps or integrity issues."
}
$Status = .\bin\agentmem.exe status --root $Evidence | ConvertFrom-Json
$Status | Select-Object workflow_ready,next_action,candidate_generation
```

List the current review queue. The CLI resolves the latest verified candidate
generation, so ordinary review commands do not need a generated path:

```powershell
$Queue = .\bin\agentmem.exe review list `
  --root $Evidence `
  --status review_ready `
  --limit 20 | ConvertFrom-Json
if ($Queue.candidates.Count -eq 0) {
  throw "No review-ready candidate was derived."
}
$Item = $Queue.candidates[0]
$Basis = @("explicit_remember", "user_correction", "stable_repetition") |
  Where-Object { $Item.candidate.support_types -contains $_ } |
  Select-Object -First 1
$Item.candidate | Select-Object candidate_id,text,support_types

$Packet = .\bin\agentmem.exe review packet `
  --root $Evidence `
  --status review_ready `
  --limit 20 | ConvertFrom-Json
if ($Packet.items -ne $Queue.candidates.Count) {
  throw "Review packet does not cover the displayed candidate queue."
}
$PacketPath = Join-Path $Evidence $Packet.relative_path
$PacketDocument = Get-Content -LiteralPath $PacketPath -Raw | ConvertFrom-Json
$PacketDocument.items[0] | Select-Object text_sha256,review_status
```

Read the candidate, packet, and evidence before recording validation. In a real
deployment, the operator must make this decision instead of asking the Agent to
approve its own output:

```powershell
.\bin\agentmem.exe review decide `
  --root $Evidence `
  --candidate $Item.candidate.candidate_id `
  --action validate `
  --reviewer local-user `
  --scope project `
  --scope-value example-project `
  --basis $Basis `
  --reason "Reviewed the source evidence and confirmed this project preference."
```

Promotion is a separate decision and rescans the exact text:

```powershell
$Promoted = .\bin\agentmem.exe promote candidate `
  --root $Evidence `
  --candidate $Item.candidate.candidate_id `
  --approver local-user `
  --packet $Packet.packet_id `
  --reason "Approved for portable project memory." | ConvertFrom-Json
```

Export into a different repository and require retrieval of the promoted
revision:

```powershell
.\bin\agentmem.exe portable init --repo $Memory
.\bin\agentmem.exe portable export --root $Evidence --repo $Memory
.\bin\agentmem.exe portable verify --repo $Memory
$Loadout = .\bin\agentmem.exe loadout create `
  --repo $Memory `
  --name "Project memory" `
  --description "Reviewed example-project memory." `
  --scope-kind project `
  --scope-value example-project `
  --agent codex `
  --memory $Promoted.revision.memory_id | ConvertFrom-Json
.\bin\agentmem.exe loadout verify `
  --repo $Memory `
  --loadout $Loadout.loadout.loadout_id
$Search = .\bin\agentmem.exe recall search `
  --root $Evidence `
  --repo $Memory `
  --agent codex `
  --scope-project example-project `
  --query $Item.candidate.text | ConvertFrom-Json
if ($Search.selected.memory_id -notcontains $Promoted.revision.memory_id) {
  throw "Retrieval did not deliver the promoted memory."
}
$Context = .\bin\agentmem.exe loadout context `
  --root $Evidence `
  --repo $Memory `
  --loadout $Loadout.loadout.loadout_id `
  --agent codex `
  --scope-project example-project | ConvertFrom-Json
if ($Context.receipt.memories.memory_id -notcontains $Promoted.revision.memory_id) {
  throw "Loadout did not deliver the promoted revision."
}
$Context.receipt | Select-Object context_receipt_id,content_sha256,content_bytes,estimated_tokens
```

After inspecting the readable portable-memory diff, connect that separate
directory to an empty private remote by following [Git synchronization](git-sync.md).

The search `injection_id` and loadout `context_receipt_id` identify the exact
delivered contexts. Delivery is not automatically counted as adoption or
usefulness.

## macOS or Linux

```sh
run_root="$(mktemp -d "${TMPDIR:-/tmp}/agentmem-demo.XXXXXX")"
evidence="$run_root/evidence"
memory="$run_root/portable-memory"
demo_sessions="$(pwd)/examples/quickstart"

./bin/agentmem compatibility --agent codex
onboard="$(./bin/agentmem onboard codex --root "$evidence" --path "$demo_sessions")"
printf '%s\n' "$onboard" | jq -e '.ready == true and .import.gaps_appended == 0' >/dev/null
./bin/agentmem status --root "$evidence" | jq '{workflow_ready,next_action,candidate_generation}'

queue="$(./bin/agentmem review list --root "$evidence" --status review_ready --limit 20)"
item="$(printf '%s\n' "$queue" | jq -ce '.candidates[0] // error("no review-ready candidate")')"
candidate_id="$(printf '%s\n' "$item" | jq -r '.candidate.candidate_id')"
query="$(printf '%s\n' "$item" | jq -r '.candidate.text')"
basis="$(printf '%s\n' "$item" | jq -r '[.candidate.support_types[] | select(. == "explicit_remember" or . == "user_correction" or . == "stable_repetition")] | first // empty')"
test -n "$basis"
printf '%s\n' "$item" | jq '{candidate_id:.candidate.candidate_id,text:.candidate.text,support_types:.candidate.support_types}'
packet="$(./bin/agentmem review packet --root "$evidence" --status review_ready --limit 20)"
packet_id="$(printf '%s\n' "$packet" | jq -r '.packet_id')"
packet_path="$evidence/$(printf '%s\n' "$packet" | jq -r '.relative_path')"
test "$(printf '%s\n' "$packet" | jq -r '.items')" = "$(printf '%s\n' "$queue" | jq -r '.candidates | length')"
jq '.items[0] | {text_sha256,review_status}' "$packet_path"

./bin/agentmem review decide \
  --root "$evidence" \
  --candidate "$candidate_id" \
  --action validate \
  --reviewer local-user \
  --scope project \
  --scope-value example-project \
  --basis "$basis" \
  --reason "Reviewed the source evidence and confirmed this project preference."

promoted="$(./bin/agentmem promote candidate \
  --root "$evidence" \
  --candidate "$candidate_id" \
  --approver local-user \
  --packet "$packet_id" \
  --reason "Approved for portable project memory.")"
memory_id="$(printf '%s\n' "$promoted" | jq -r '.revision.memory_id')"

./bin/agentmem portable init --repo "$memory"
./bin/agentmem portable export --root "$evidence" --repo "$memory"
./bin/agentmem portable verify --repo "$memory"
loadout="$(./bin/agentmem loadout create \
  --repo "$memory" \
  --name "Project memory" \
  --description "Reviewed example-project memory." \
  --scope-kind project \
  --scope-value example-project \
  --agent codex \
  --memory "$memory_id")"
loadout_id="$(printf '%s\n' "$loadout" | jq -r '.loadout.loadout_id')"
./bin/agentmem loadout verify --repo "$memory" --loadout "$loadout_id"
search="$(./bin/agentmem recall search \
  --root "$evidence" \
  --repo "$memory" \
  --agent codex \
  --scope-project example-project \
  --query "$query")"
printf '%s\n' "$search" | jq -e --arg id "$memory_id" '.selected | any(.memory_id == $id)' >/dev/null
context="$(./bin/agentmem loadout context \
  --root "$evidence" \
  --repo "$memory" \
  --loadout "$loadout_id" \
  --agent codex \
  --scope-project example-project)"
printf '%s\n' "$context" | jq -e --arg id "$memory_id" '.receipt.memories | any(.memory_id == $id)' >/dev/null
printf '%s\n' "$context" | jq '.receipt | {context_receipt_id,content_sha256,content_bytes,estimated_tokens}'
```

After inspecting the readable portable-memory diff, connect that separate
directory to an empty private remote by following [Git synchronization](git-sync.md).

## Use existing Codex history

After the synthetic walkthrough succeeds, create a new evidence directory and
point `onboard` at your actual sessions directory:

```powershell
$CodexRoot = if ($env:CODEX_HOME) { $env:CODEX_HOME } else { Join-Path $HOME ".codex" }
.\bin\agentmem.exe onboard codex --root <new-evidence-directory> --path (Join-Path $CodexRoot "sessions")
```

```sh
./bin/agentmem onboard codex \
  --root <new-evidence-directory> \
  --path "${CODEX_HOME:-$HOME/.codex}/sessions"
```

Real history may legitimately produce no review-ready candidates. That is a
safe result. Do not manufacture a memory or weaken the evidence policy. If new
history is captured later, rerun `onboard` or the explicit derive commands.

## Other Agents

Claude Code and OpenCode sources use the same evidence and portable-memory
protocols:

```text
agentmem import claude-home --root <evidence> --path <claude-home>
agentmem import opencode-export --root <evidence> --path <export-file-or-directory>
agentmem import opencode-events --root <evidence> --path <plugin-spool-directory>
agentmem import deepseek-harness-events --root <evidence> --path <bundle-spool-directory>
```

OpenCode is optional. Its pinned runtime smoke runs only on a disposable
GitHub-hosted Ubuntu runner; it is not installed by this walkthrough.

## Repeat reviewed context

After promotion and portable export, create an immutable loadout for the exact
active revisions you intend to repeat:

```text
agentmem loadout create --repo <portable-memory-directory> \
  --name "Project memory" --scope-kind project \
  --scope-value example-project --agent codex --memory <memory-id>

agentmem loadout context --root <local-evidence-directory> \
  --repo <portable-memory-directory> --loadout <loadout-id> \
  --agent codex --scope-project example-project
```

A superseded or revoked revision makes that loadout stale. Create a new
loadout after reviewing the new revision; the old artifact is retained for
audit and never follows the new head silently.

For a compact, immutable review surface before promotion:

```text
agentmem review packet --root <local-evidence-directory> \
  --status review_ready --limit 20
```

The packet does not validate or promote anything. It can be supplied later as
`promote candidate --packet <packet-path-or-id>` after the separate validation
decision.

Run a local read-only status view with:

```text
agentmem serve dashboard --root <local-evidence-directory> \
  --repo <portable-memory-directory>
```

Native Codex execution and prospective observation require explicit local-only
JSON inputs. Follow [native execution](native-execution.md) and
[longitudinal studies](longitudinal-study.md) rather than improvising receipts.

## Recovery and schemas

Run integrity checks after capture, before promotion, and after synchronization:

```text
agentmem doctor --root <evidence>
agentmem review verify --root <evidence>
agentmem promote verify --root <evidence>
agentmem portable verify --repo <portable-memory>
agentmem sync verify --repo <portable-memory>
agentmem recall verify --root <evidence>
```

If doctor reports `writer_lock_present`, first confirm that no writer is active.
Only then use `doctor --clear-stale-writer-lock`.

Every versioned JSON envelope in this walkthrough has a public schema under
`schemas/`. Encrypted evidence backup and no-overwrite restore are documented
in [backup-recovery.md](backup-recovery.md).
