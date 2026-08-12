[CmdletBinding()]
param(
  [Parameter(Mandatory = $true)]
  [string]$WorkRoot,
  [string]$BinaryPath = '',
  [string]$CodexPath = 'codex',
  [string]$Model = 'default',
  [ValidateRange(30, 3600)]
  [int]$TimeoutSeconds = 180
)

$ErrorActionPreference = 'Stop'

function Invoke-AgentmemJSON {
  param([Parameter(ValueFromRemainingArguments = $true)][string[]]$Arguments)
  $output = & $script:Binary @Arguments
  if ($LASTEXITCODE -ne 0) { throw "agentmem command failed: $($Arguments -join ' ')" }
  return $output | ConvertFrom-Json
}

function Get-SHA256Hex {
  param([Parameter(Mandatory = $true)][string]$Text)
  $algorithm = [System.Security.Cryptography.SHA256]::Create()
  try {
    $bytes = [System.Text.Encoding]::UTF8.GetBytes($Text)
    return ([System.BitConverter]::ToString($algorithm.ComputeHash($bytes))).Replace('-', '').ToLowerInvariant()
  }
  finally {
    $algorithm.Dispose()
  }
}

$Repository = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
$SuitePath = Join-Path $Repository 'evals\codex-memory-v1\suite.json'
$OracleSource = Join-Path $Repository 'evals\codex-memory-v1\oracle'
$Go = if ($env:AGENTMEM_GO) { $env:AGENTMEM_GO } else { 'go' }
$AbsoluteWorkRoot = [System.IO.Path]::GetFullPath($WorkRoot)
if (Test-Path -LiteralPath $AbsoluteWorkRoot) {
  throw 'benchmark work root already exists; provide a new directory'
}
New-Item -ItemType Directory -Path $AbsoluteWorkRoot | Out-Null

$Evidence = Join-Path $AbsoluteWorkRoot 'evidence'
$Portable = Join-Path $AbsoluteWorkRoot 'portable-memory'
$Sessions = Join-Path $AbsoluteWorkRoot 'sessions'
$Workspace = Join-Path $AbsoluteWorkRoot 'workspace'
$Overlays = Join-Path $AbsoluteWorkRoot 'oracle-overlays'
$Results = Join-Path $AbsoluteWorkRoot 'results'
$PlanPath = Join-Path $AbsoluteWorkRoot 'plan.json'
$script:Binary = Join-Path $AbsoluteWorkRoot 'agentmem.exe'
$Oracle = Join-Path $AbsoluteWorkRoot 'exact-oracle.exe'
New-Item -ItemType Directory -Force -Path $Sessions, $Workspace, $Overlays | Out-Null

$OriginalLocation = Get-Location
Set-Location -LiteralPath $Repository
try {
  if ([string]::IsNullOrWhiteSpace($BinaryPath)) {
    & $Go build -o $script:Binary .\cmd\agentmem
    if ($LASTEXITCODE -ne 0) { throw 'agentmem build failed' }
  } else {
    Copy-Item -LiteralPath (Resolve-Path -LiteralPath $BinaryPath) -Destination $script:Binary
  }
  & $Go build -o $Oracle $OracleSource
  if ($LASTEXITCODE -ne 0) { throw 'benchmark oracle build failed' }

  $Suite = Get-Content -Raw -LiteralPath $SuitePath | ConvertFrom-Json
  $Tasks = @($Suite.tasks)
  if ($Suite.schema_version -ne 'codex-memory-benchmark-suite/v1alpha1' -or
      $Suite.tool_policy -ne 'forbid' -or $Tasks.Count -ne 20) {
    throw 'frozen benchmark suite is invalid'
  }

  $utf8 = [System.Text.UTF8Encoding]::new($false)
  for ($index = 0; $index -lt $Tasks.Count; $index++) {
    $task = $Tasks[$index]
    $sessionID = '00000000-0000-0000-0000-' + ($index + 1).ToString('000000000000')
    $start = [DateTimeOffset]::Parse('2026-08-12T00:00:00Z').AddMinutes($index)
    $events = @(
      [ordered]@{ timestamp = $start.ToString('o'); type = 'session_meta'; payload = [ordered]@{ id = $sessionID } },
      [ordered]@{ timestamp = $start.AddSeconds(1).ToString('o'); type = 'event_msg'; payload = [ordered]@{ type = 'user_message'; message = $task.memory_text } },
      [ordered]@{ timestamp = $start.AddSeconds(2).ToString('o'); type = 'response_item'; payload = [ordered]@{ type = 'message'; role = 'assistant'; content = @([ordered]@{ type = 'output_text'; text = 'Recorded.' }) } }
    )
    $lines = $events | ForEach-Object { $_ | ConvertTo-Json -Compress -Depth 8 }
    $rollout = Join-Path $Sessions ("rollout-{0}-{1}.jsonl" -f $start.ToString('yyyy-MM-ddTHH-mm-ss'), $sessionID)
    [System.IO.File]::WriteAllLines($rollout, $lines, $utf8)
  }

  $Onboard = Invoke-AgentmemJSON onboard codex --root $Evidence --path $Sessions
  if (-not $Onboard.ready -or $Onboard.import.gaps_appended -ne 0 -or $Onboard.candidates.review_ready -ne 20) {
    throw 'benchmark evidence did not produce the frozen 20-candidate population'
  }
  $Queue = Invoke-AgentmemJSON review list --root $Evidence --status review_ready --limit 50
  if (@($Queue.candidates).Count -ne 20) { throw 'benchmark review queue is incomplete' }

  foreach ($task in $Tasks) {
    $derivedText = $task.memory_text.TrimEnd('.')
    $matches = @($Queue.candidates | Where-Object { $_.candidate.text -ceq $derivedText })
    if ($matches.Count -ne 1) { throw "benchmark candidate mismatch for $($task.task_id)" }
    $item = $matches[0]
    Invoke-AgentmemJSON review decide --root $Evidence --candidate $item.candidate.candidate_id `
      --action validate --reviewer frozen-suite --reviewer-kind synthetic_test --scope project `
      --scope-value $Suite.project_scope --basis explicit_remember `
      --reason 'Synthetic validation for the frozen public capability suite.' | Out-Null
    Invoke-AgentmemJSON promote candidate --root $Evidence --candidate $item.candidate.candidate_id `
      --approver frozen-suite --approver-kind synthetic_test --confirm-text-sha256 $item.text_sha256 `
      --reason 'Synthetic promotion for the frozen public capability suite.' | Out-Null
  }

  Invoke-AgentmemJSON portable init --repo $Portable | Out-Null
  $Export = Invoke-AgentmemJSON portable export --root $Evidence --repo $Portable
  $Verified = Invoke-AgentmemJSON portable verify --repo $Portable
  if ($Export.revisions_written -ne 20 -or $Verified.active_memories -ne 20) {
    throw 'benchmark portable memory population is incomplete'
  }

  [System.IO.File]::WriteAllText((Join-Path $Workspace 'README.md'), "# Frozen Codex memory capability task`n", $utf8)
  $PlanTasks = @()
  foreach ($task in $Tasks) {
    $context = Invoke-AgentmemJSON recall context --root $Evidence --repo $Portable --agent codex `
      --scope-project $Suite.project_scope --query $task.retrieval_query --limit 1 `
      --token-budget 256 --byte-budget 2048 --channel harness
    $selected = @($context.retrieval.selected)
    if ($selected.Count -ne 1 -or $selected[0].text -cne $task.memory_text.TrimEnd('.') -or [string]::IsNullOrWhiteSpace($context.injection_id)) {
      throw "verified retrieval did not select the exact memory for $($task.task_id)"
    }
    $overlay = Join-Path $Overlays $task.task_id
    New-Item -ItemType Directory -Path $overlay | Out-Null
    [System.IO.File]::WriteAllText((Join-Path $overlay 'oracle-boundary.txt'), "No plaintext answer is stored in the oracle overlay.`n", $utf8)
    $PlanTasks += [ordered]@{
      task_id = $task.task_id
      cluster_id = $task.cluster_id
      prompt = $task.prompt
      tool_policy = $Suite.tool_policy
      injection_id = $context.injection_id
      workspace = 'workspace'
      oracle_overlay = ('oracle-overlays/' + $task.task_id)
      oracle_command = @($Oracle, (Get-SHA256Hex -Text $task.expected.Trim()))
    }
  }

  $Plan = [ordered]@{
    schema_version = 'codex-memory-benchmark-plan/v1alpha1'
    suite_id = $Suite.suite_id
    model = $Model
    timeout_seconds = $TimeoutSeconds
    tasks = $PlanTasks
    privacy = 'local_only'
  }
  [System.IO.File]::WriteAllText($PlanPath, ($Plan | ConvertTo-Json -Depth 8) + "`n", $utf8)

  $Report = Invoke-AgentmemJSON eval codex benchmark --root $Evidence --file $PlanPath `
    --codex $CodexPath --output $Results --confirm-oracle-execution
  if ($Report.summary.pairs -ne 20 -or $Report.summary.distinct_clusters -ne 20 -or `
      $Report.summary.verified_retrieval_pairs -ne 20) {
    throw 'benchmark report does not cover the frozen verified-retrieval population'
  }
  $Report | ConvertTo-Json -Depth 12
}
finally {
  Set-Location -LiteralPath $OriginalLocation
}
