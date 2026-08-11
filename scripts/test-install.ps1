[CmdletBinding()]
param()

$ErrorActionPreference = "Stop"
Set-StrictMode -Version Latest

if (-not [System.Runtime.InteropServices.RuntimeInformation]::IsOSPlatform(
        [System.Runtime.InteropServices.OSPlatform]::Windows)) {
    throw "The Windows installer test requires Windows."
}

$repositoryRoot = Split-Path -Parent $PSScriptRoot
$temporaryRoot = Join-Path ([System.IO.Path]::GetTempPath()) (
    "agentmem-installer-test-" + [guid]::NewGuid().ToString("N")
)
$artifactRoot = Join-Path $temporaryRoot "artifacts"
$installRoot = Join-Path $temporaryRoot "installed"
$sentinel = Join-Path $temporaryRoot "evidence-sentinel.txt"
$previousAssetRoot = $env:AGENTMEM_TEST_ASSET_ROOT
$previousCGO = $env:CGO_ENABLED

function New-TestRelease {
    param(
        [Parameter(Mandatory = $true)][string]$Version,
        [string]$BinaryVersion = ""
    )

    if ([string]::IsNullOrWhiteSpace($BinaryVersion)) {
        $BinaryVersion = $Version
    }

    $architecture = [System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture.ToString()
    switch ($architecture) {
        "X64" { $releaseArchitecture = "amd64" }
        "Arm64" { $releaseArchitecture = "arm64" }
        default { throw "Unsupported Windows test architecture: $architecture" }
    }
    $asset = "agentmem_${Version}_windows_${releaseArchitecture}.zip"
    $packageRoot = Join-Path $temporaryRoot ("package-" + $Version)
    New-Item -ItemType Directory -Force -Path $packageRoot, $artifactRoot | Out-Null
    $binary = Join-Path $packageRoot "agentmem.exe"

    Push-Location $repositoryRoot
    try {
        $env:CGO_ENABLED = "0"
        & go build -trimpath -buildvcs=false -ldflags "-s -w -X main.version=$BinaryVersion" `
            -o $binary ./cmd/agentmem
        if ($LASTEXITCODE -ne 0) {
            throw "Building the Windows installer fixture failed."
        }
    }
    finally {
        Pop-Location
    }

    $archive = Join-Path $artifactRoot $asset
    Compress-Archive -LiteralPath $binary -DestinationPath $archive
    $digest = (Get-FileHash -Algorithm SHA256 -LiteralPath $archive).Hash.ToLowerInvariant()
    Set-Content -LiteralPath (Join-Path $artifactRoot "SHA256SUMS") `
        -Value "$digest  $asset" -Encoding ascii
    return $asset
}

function Invoke-WebRequest {
    param(
        [hashtable]$Headers,
        [Parameter(Mandatory = $true)][string]$Uri,
        [Parameter(Mandatory = $true)][string]$OutFile
    )

    $name = [System.IO.Path]::GetFileName(([uri]$Uri).AbsolutePath)
    $source = Join-Path $env:AGENTMEM_TEST_ASSET_ROOT $name
    if (-not (Test-Path -LiteralPath $source -PathType Leaf)) {
        throw "Unknown installer test asset: $name"
    }
    Copy-Item -LiteralPath $source -Destination $OutFile
}

New-Item -ItemType Directory -Path $temporaryRoot, $installRoot | Out-Null
Set-Content -LiteralPath $sentinel -Value "preserve local evidence" -Encoding utf8
$env:AGENTMEM_TEST_ASSET_ROOT = $artifactRoot

try {
    foreach ($version in @("v0.0.0-test", "v0.0.1-test")) {
        $asset = New-TestRelease -Version $version
        & (Join-Path $PSScriptRoot "install.ps1") -Version $version -InstallDir $installRoot | Out-Null
        $installed = Join-Path $installRoot "agentmem.exe"
        $versionReport = (& $installed version | ConvertFrom-Json)
        if ($versionReport.version -ne $version) {
            throw "Installed version $($versionReport.version) does not match $version."
        }
        if ((Get-Content -Raw -LiteralPath $sentinel).Trim() -ne "preserve local evidence") {
            throw "Installer changed data outside the binary directory."
        }
    }

    $installed = Join-Path $installRoot "agentmem.exe"
    $installedDigest = (Get-FileHash -Algorithm SHA256 -LiteralPath $installed).Hash
    Add-Content -LiteralPath (Join-Path $artifactRoot $asset) -Value "tampered" -Encoding ascii
    $tamperRejected = $false
    try {
        & (Join-Path $PSScriptRoot "install.ps1") -Version "v0.0.1-test" `
            -InstallDir $installRoot | Out-Null
    }
    catch {
        $tamperRejected = $_.Exception.Message -like "*checksum verification failed*"
    }
    if (-not $tamperRejected) {
        throw "Installer did not reject a release archive with the wrong checksum."
    }
    if ((Get-FileHash -Algorithm SHA256 -LiteralPath $installed).Hash -ne $installedDigest) {
        throw "Failed checksum verification changed the installed binary."
    }

    $null = New-TestRelease -Version "v0.0.2-test" -BinaryVersion "v9.9.9-test"
    $versionMismatchRejected = $false
    try {
        & (Join-Path $PSScriptRoot "install.ps1") -Version "v0.0.2-test" `
            -InstallDir $installRoot | Out-Null
    }
    catch {
        $versionMismatchRejected = $_.Exception.Message -like "*does not match requested version*"
    }
    if (-not $versionMismatchRejected) {
        throw "Installer did not reject a release whose binary version mismatched its tag."
    }
    if ((Get-FileHash -Algorithm SHA256 -LiteralPath $installed).Hash -ne $installedDigest) {
        throw "Failed version verification changed the installed binary."
    }

    $installedFiles = @(Get-ChildItem -LiteralPath $installRoot -Force)
    if ($installedFiles.Count -ne 1 -or $installedFiles[0].Name -ne "agentmem.exe") {
        throw "Installer left unexpected files in the binary directory."
    }
}
finally {
    $env:AGENTMEM_TEST_ASSET_ROOT = $previousAssetRoot
    $env:CGO_ENABLED = $previousCGO
    if ((Test-Path -LiteralPath $temporaryRoot) -and
        $temporaryRoot.StartsWith([System.IO.Path]::GetTempPath(), [System.StringComparison]::OrdinalIgnoreCase)) {
        Remove-Item -Recurse -Force -LiteralPath $temporaryRoot
    }
}
