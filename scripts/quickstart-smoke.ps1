$ErrorActionPreference = 'Stop'

$Repository = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
$TemporaryParent = [System.IO.Path]::GetFullPath([System.IO.Path]::GetTempPath())
$Root = Join-Path $TemporaryParent ("agentmem-quickstart-" + [guid]::NewGuid().ToString('N'))
$Evidence = Join-Path $Root 'evidence'
$Memory = Join-Path $Root 'portable-memory'
$Binary = Join-Path $Root 'agentmem.exe'
$DemoSessions = Join-Path $Repository 'examples\quickstart'
$Go = if ($env:AGENTMEM_GO) { $env:AGENTMEM_GO } else { 'go' }
New-Item -ItemType Directory -Force $Root | Out-Null
$OriginalLocation = Get-Location
Set-Location -LiteralPath $Repository

try {
  & $Go build -o $Binary .\cmd\agentmem
  if ($LASTEXITCODE -ne 0) { throw 'agentmem build failed' }

  & $Binary init --root $Evidence | Out-Null
  $Import = & $Binary import codex --root $Evidence --path $DemoSessions | ConvertFrom-Json
  $Doctor = & $Binary doctor --root $Evidence | ConvertFrom-Json
  if ($Import.schema_version -ne 'agent-history-import-result/v1alpha1') {
    throw 'history import returned an unsupported schema version'
  }
  if ($Import.gaps_appended -ne 0 -or -not $Doctor.ready) {
    throw 'quickstart evidence import did not verify cleanly'
  }

  $Episodes = & $Binary derive episodes --root $Evidence | ConvertFrom-Json
  $Candidates = & $Binary derive candidates --root $Evidence --episodes $Episodes.generation_path | ConvertFrom-Json
  $Queue = & $Binary review list --root $Evidence --candidates $Candidates.generation_path --status review_ready --limit 20 | ConvertFrom-Json
  if ($Queue.candidates.Count -eq 0) { throw 'quickstart produced no review-ready candidate' }
  $Item = $Queue.candidates[0]
  $Basis = @('explicit_remember', 'user_correction', 'stable_repetition') |
    Where-Object { $Item.candidate.support_types -contains $_ } |
    Select-Object -First 1
  if (-not $Basis) { throw 'quickstart candidate has no supported review basis' }

  & $Binary review decide --root $Evidence --candidates $Candidates.generation_path --candidate $Item.candidate.candidate_id --action validate --reviewer quickstart-smoke --scope project --scope-value example-project --basis $Basis --reason 'Reviewed synthetic source evidence.' | Out-Null
  $Promoted = & $Binary promote candidate --root $Evidence --candidates $Candidates.generation_path --candidate $Item.candidate.candidate_id --approver quickstart-smoke --confirm-text-sha256 $Item.text_sha256 --reason 'Approved synthetic portable project memory.' | ConvertFrom-Json

  & $Binary portable init --repo $Memory | Out-Null
  $Export = & $Binary portable export --root $Evidence --repo $Memory | ConvertFrom-Json
  $Portable = & $Binary portable verify --repo $Memory | ConvertFrom-Json
  $Recall = & $Binary recall search --root $Evidence --repo $Memory --agent codex --scope-project example-project --query $Item.candidate.text | ConvertFrom-Json
  if ($Recall.selected.memory_id -notcontains $Promoted.revision.memory_id) {
    throw 'retrieval did not select the promoted memory'
  }

  [pscustomobject]@{
    schema_version = 'quickstart-smoke/v1alpha1'
    gaps_appended = $Import.gaps_appended
    doctor_ready = $Doctor.ready
    review_ready = $Candidates.review_ready
    revisions_written = $Export.revisions_written
    active_memories = $Portable.active_memories
    selected_memories = $Recall.selected.Count
  } | ConvertTo-Json -Compress
}
finally {
  Set-Location -LiteralPath $OriginalLocation
  $ResolvedRoot = [System.IO.Path]::GetFullPath($Root)
  if ($ResolvedRoot.StartsWith($TemporaryParent, [System.StringComparison]::OrdinalIgnoreCase) -and
      (Split-Path -Leaf $ResolvedRoot).StartsWith('agentmem-quickstart-')) {
    Remove-Item -LiteralPath $ResolvedRoot -Recurse -Force -ErrorAction SilentlyContinue
  }
}
