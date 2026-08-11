[CmdletBinding()]
param(
    [string]$Version = "latest",
    [string]$InstallDir = ""
)

$ErrorActionPreference = "Stop"
$repository = "rrrrrredy/agent-memory-system"

function Move-AgentmemFile {
    param(
        [Parameter(Mandatory = $true)][string]$Source,
        [Parameter(Mandatory = $true)][string]$Destination
    )
    for ($attempt = 1; $attempt -le 30; $attempt++) {
        try {
            Move-Item -Force -LiteralPath $Source -Destination $Destination
            return
        }
        catch {
            if ($attempt -eq 30) {
                throw
            }
            Start-Sleep -Milliseconds 100
        }
    }
}

function Remove-AgentmemFile {
    param([Parameter(Mandatory = $true)][string]$Path)
    for ($attempt = 1; $attempt -le 30; $attempt++) {
        try {
            Remove-Item -Force -LiteralPath $Path
            return
        }
        catch {
            if ($attempt -eq 30) {
                throw
            }
            Start-Sleep -Milliseconds 100
        }
    }
}

if ([string]::IsNullOrWhiteSpace($InstallDir)) {
    if ([string]::IsNullOrWhiteSpace($env:LOCALAPPDATA)) {
        $InstallDir = Join-Path $env:USERPROFILE ".local\bin"
    }
    else {
        $InstallDir = Join-Path $env:LOCALAPPDATA "Programs\agentmem"
    }
}

$architecture = [System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture.ToString()
switch ($architecture) {
    "X64" { $releaseArchitecture = "amd64" }
    "Arm64" { $releaseArchitecture = "arm64" }
    default { throw "Unsupported Windows architecture: $architecture" }
}

$headers = @{ "User-Agent" = "agent-memory-system-installer" }
if ($Version -eq "latest") {
    $release = Invoke-RestMethod -Headers $headers -Uri "https://api.github.com/repos/$repository/releases/latest"
    $Version = [string]$release.tag_name
}
if ($Version -notmatch '^v[0-9][0-9A-Za-z._-]*$') {
    throw "Invalid release version: $Version"
}

$asset = "agentmem_${Version}_windows_${releaseArchitecture}.zip"
$baseUrl = "https://github.com/$repository/releases/download/$Version"
$temporaryRoot = Join-Path ([System.IO.Path]::GetTempPath()) ("agentmem-install-" + [guid]::NewGuid().ToString("N"))
$archivePath = Join-Path $temporaryRoot $asset
$checksumPath = Join-Path $temporaryRoot "SHA256SUMS"
$extractPath = Join-Path $temporaryRoot "extract"

New-Item -ItemType Directory -Path $temporaryRoot | Out-Null
try {
    Invoke-WebRequest -Headers $headers -Uri "$baseUrl/$asset" -OutFile $archivePath
    Invoke-WebRequest -Headers $headers -Uri "$baseUrl/SHA256SUMS" -OutFile $checksumPath

    $escapedAsset = [regex]::Escape($asset)
    $checksumLine = Get-Content -LiteralPath $checksumPath | Where-Object {
        $_ -match "^([a-fA-F0-9]{64})\s+\*?(?:\./)?$escapedAsset$"
    }
    if (@($checksumLine).Count -ne 1) {
        throw "Release checksum entry is missing or duplicated for $asset"
    }
    $expected = ([regex]::Match([string]$checksumLine, '^[a-fA-F0-9]{64}')).Value.ToLowerInvariant()
    $actual = (Get-FileHash -Algorithm SHA256 -LiteralPath $archivePath).Hash.ToLowerInvariant()
    if ($actual -ne $expected) {
        throw "Release checksum verification failed for $asset"
    }

    Expand-Archive -LiteralPath $archivePath -DestinationPath $extractPath
    $binaries = @(Get-ChildItem -LiteralPath $extractPath -Recurse -File -Filter "agentmem.exe")
    if ($binaries.Count -ne 1) {
        throw "Release archive does not contain exactly one agentmem.exe"
    }

    New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
    $target = Join-Path $InstallDir "agentmem.exe"
    $staged = Join-Path $InstallDir (".agentmem-" + [guid]::NewGuid().ToString("N") + ".tmp.exe")
    Copy-Item -LiteralPath $binaries[0].FullName -Destination $staged
    try {
        $versionReport = (& $staged version | ConvertFrom-Json)
        if ([string]$versionReport.version -ne $Version) {
            throw "Release binary version $($versionReport.version) does not match requested version $Version"
        }
        Move-AgentmemFile -Source $staged -Destination $target
    }
    finally {
        if (Test-Path -LiteralPath $staged) {
            Remove-AgentmemFile -Path $staged
        }
    }
    Write-Output "Installed $Version to $target"
    Write-Output "Add $InstallDir to PATH if it is not already available."
}
finally {
    if ((Test-Path -LiteralPath $temporaryRoot) -and
        $temporaryRoot.StartsWith([System.IO.Path]::GetTempPath(), [System.StringComparison]::OrdinalIgnoreCase)) {
        Remove-Item -Recurse -Force -LiteralPath $temporaryRoot
    }
}
