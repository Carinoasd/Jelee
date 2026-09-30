#requires -Version 7.2
param([switch]$Offline)
. "$PSScriptRoot/media-tools-lib.ps1"
$mediaRoot = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..'))
$selected = Get-MediaSpec $mediaRoot
$spec = $selected.Spec
$paths = Get-MediaPaths $mediaRoot $selected
foreach ($path in @($paths.Archive, $paths.Install, $paths.Record)) {
    [IO.Directory]::CreateDirectory([IO.Path]::GetDirectoryName($path)) | Out-Null
}
$lockPath = Assert-LocalPath $mediaRoot ([IO.Path]::ChangeExtension($paths.Record, '.lock'))
try { $mediaLock = [IO.File]::Open($lockPath, [IO.FileMode]::OpenOrCreate, [IO.FileAccess]::ReadWrite, [IO.FileShare]::None) }
catch { throw 'Another media bootstrap is running or its lock is inaccessible' }
try {
    if (-not (Test-Path -LiteralPath $paths.Archive)) {
        if ($Offline) { throw 'Offline media archive is missing' }
        $uri = $spec.url
        if ($env:JELEE_TOOLS_MIRROR) {
            Assert-MediaHTTPS $env:JELEE_TOOLS_MIRROR
            $uri = $env:JELEE_TOOLS_MIRROR.TrimEnd('/') + '/' + [IO.Path]::GetFileName($paths.Archive)
        }
        $partial = Assert-LocalPath $mediaRoot ($paths.Archive + '.' + [Guid]::NewGuid().ToString('N') + '.partial')
        try {
            & curl.exe --fail --location --proto '=https' --proto-redir '=https' --connect-timeout 30 --max-time 300 --max-filesize $spec.sizeBytes --retry 2 --output $partial $uri
            if ($LASTEXITCODE -ne 0) { throw 'Media download failed' }
            if ((Get-Item -LiteralPath $partial).Length -ne $spec.sizeBytes) { throw 'Media archive size differs from manifest' }
            Assert-ArchiveHash $partial $spec.sha256
            Move-Item -LiteralPath $partial -Destination $paths.Archive
        } finally { if (Test-Path -LiteralPath $partial) { Remove-Item -LiteralPath $partial -Force } }
    }
    if ((Get-Item -LiteralPath $paths.Archive).PSIsContainer) { throw 'Media cache is not an archive file' }
    try {
        Assert-ArchiveHash $paths.Archive $spec.sha256
        if ((Get-Item -LiteralPath $paths.Archive).Length -ne $spec.sizeBytes) { throw 'Media archive size differs from manifest' }
    } catch {
        # Delete only the known bad archive below this project, never an install.
        $badArchive = Assert-LocalPath $mediaRoot $paths.Archive
        Remove-Item -LiteralPath $badArchive -Force
        throw
    }
    if (-not (Test-Path -LiteralPath $paths.Install)) {
        $stage = Assert-LocalPath $mediaRoot ($paths.Install + '.staging-' + [Guid]::NewGuid().ToString('N'))
        [IO.Directory]::CreateDirectory($stage) | Out-Null
        try {
            Expand-SafeZip $paths.Archive $stage
            Assert-MediaFiles $mediaRoot $spec $stage
            foreach ($name in @('ffmpeg', 'ffprobe')) { Assert-MediaVersion $mediaRoot $spec $stage $name }
            Move-Item -LiteralPath $stage -Destination (Assert-LocalPath $mediaRoot $paths.Install)
        } finally { Remove-LocalTree $mediaRoot $stage }
    }
    Assert-MediaFiles $mediaRoot $spec $paths.Install
    foreach ($name in @('ffmpeg', 'ffprobe')) { Assert-MediaVersion $mediaRoot $spec $paths.Install $name }
    $record = @{
        schemaVersion = 1; platform = $selected.Platform; vendorVersion = $spec.vendorVersion
        archiveSHA256 = $spec.sha256; executables = $spec.executables; licenseFiles = $spec.licenseFiles
        installedAt = [DateTime]::UtcNow.ToString('o')
    }
    $recordPartial = Assert-LocalPath $mediaRoot ($paths.Record + '.' + [Guid]::NewGuid().ToString('N') + '.partial')
    try {
        $record | ConvertTo-Json -Depth 8 | Set-Content -LiteralPath $recordPartial -Encoding utf8NoBOM
        Move-Item -LiteralPath $recordPartial -Destination $paths.Record -Force
    } finally { if (Test-Path -LiteralPath $recordPartial) { Remove-Item -LiteralPath $recordPartial -Force } }
    $binDir = Assert-LocalPath $mediaRoot (Join-Path $mediaRoot '.bin')
    [IO.Directory]::CreateDirectory($binDir) | Out-Null
    foreach ($name in @('ffmpeg', 'ffprobe')) {
        $wrapper = Assert-LocalPath $mediaRoot (Join-Path $binDir "$name.cmd")
        "@echo off`r`npwsh -NoProfile -File `"%~dp0..\scripts\run-media-tool.ps1`" -Tool $name %*`r`nexit /b %errorlevel%`r`n" |
            Set-Content -LiteralPath $wrapper -Encoding utf8NoBOM
    }
} finally { $mediaLock.Dispose() }
Test-MediaInstallation $mediaRoot
