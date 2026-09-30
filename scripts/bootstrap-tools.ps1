#requires -Version 7.2
[CmdletBinding()]
param([switch]$Offline)
. "$PSScriptRoot/toolchain-lib.ps1"
$root = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..'))
$selected = Get-GoSpec $root
$spec = $selected.Spec
$toolsDir = Assert-LocalPath $root (Join-Path $root '.tools')
$binDir = Assert-LocalPath $root (Join-Path $root '.bin')
$downloadDir = Assert-LocalPath $root (Join-Path $toolsDir 'downloads')
$installDir = Assert-LocalPath $toolsDir (Join-Path $toolsDir $spec.installPath)
foreach ($dir in @($toolsDir, $binDir, $downloadDir)) { [IO.Directory]::CreateDirectory($dir) | Out-Null }
$archiveName = [IO.Path]::GetFileName(([Uri]$spec.url).AbsolutePath)
$archive = Assert-LocalPath $toolsDir (Join-Path $downloadDir $archiveName)

if (-not (Test-Path -LiteralPath $archive)) {
    if ($Offline) { throw "Offline cache missing: $archiveName" }
    $uri = $spec.url
    if ($env:JELEE_TOOLS_MIRROR) {
        if ($env:JELEE_TOOLS_MIRROR -notmatch '^https://') { throw 'JELEE_TOOLS_MIRROR must use HTTPS' }
        $uri = $env:JELEE_TOOLS_MIRROR.TrimEnd('/') + '/' + $archiveName
    }
    $partial = Assert-LocalPath $toolsDir ($archive + '.partial')
    try {
        Write-Host "Downloading $archiveName"
        # curl checks certificates and rejects HTTP redirects. Proxy environment variables are supported.
        & curl.exe --fail --location --proto '=https' --proto-redir '=https' --retry 2 --output $partial $uri
        if ($LASTEXITCODE -ne 0) { throw "Download failed: curl exit $LASTEXITCODE" }
        Assert-ArchiveHash $partial $spec.sha256
        Move-Item -LiteralPath $partial -Destination $archive
    } finally {
        if (Test-Path -LiteralPath $partial) { Remove-Item -LiteralPath $partial -Force }
    }
}
try { Assert-ArchiveHash $archive $spec.sha256 } catch {
    Remove-Item -LiteralPath $archive -Force
    throw
}
$exe = Assert-LocalPath $toolsDir (Join-Path $installDir $spec.executable)
if (-not (Test-Path -LiteralPath $exe)) {
    $stage = Assert-LocalPath $toolsDir ($installDir + '.staging')
    Remove-LocalTree $toolsDir $stage
    [IO.Directory]::CreateDirectory($stage) | Out-Null
    try {
        Expand-SafeZip $archive $stage
        if (-not (Test-Path -LiteralPath (Join-Path $stage $spec.executable))) { throw 'Go binary absent from archive' }
        Remove-LocalTree $toolsDir $installDir
        Move-Item -LiteralPath $stage -Destination $installDir
    } finally { Remove-LocalTree $toolsDir $stage }
}
Assert-GoExecutablesFromZip $archive $installDir
@'
@echo off
pwsh -NoProfile -File "%~dp0..\scripts\run-go.ps1" %*
exit /b %errorlevel%
'@ | Set-Content -LiteralPath (Join-Path $binDir 'go.cmd') -Encoding utf8NoBOM
$record = @{
    schemaVersion = 1
    name = 'go'
    version = $selected.Tool.version
    platform = $selected.Platform
    archiveSHA256 = $spec.sha256
    executableSHA256 = (Get-FileHash -LiteralPath $exe -Algorithm SHA256).Hash.ToLowerInvariant()
    installedAt = [DateTime]::UtcNow.ToString('o')
}
$recordPath = Assert-LocalPath $toolsDir (Join-Path $toolsDir '.installed.json')
$installed = @{ schemaVersion = 1; platforms = @{} }
if (Test-Path -LiteralPath $recordPath) {
    $prior = Get-Content -LiteralPath $recordPath -Raw | ConvertFrom-Json -AsHashtable
    if ($prior.ContainsKey('platforms')) { $installed = $prior }
}
$installed.platforms[$selected.Platform] = $record
$installed | ConvertTo-Json -Depth 5 | Set-Content -LiteralPath $recordPath -Encoding utf8NoBOM
& "$PSScriptRoot/run-go.ps1" telemetry off
if ($LASTEXITCODE -ne 0) { throw 'Failed to disable local toolchain telemetry' }
& "$PSScriptRoot/tools-verify.ps1"
