#requires -Version 7.2
. "$PSScriptRoot/media-tools-lib.ps1"
$mediaRoot = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..'))
$scratch = Assert-LocalPath $mediaRoot (Join-Path $mediaRoot ('.tools/media-tests-' + [Guid]::NewGuid().ToString('N')))
[IO.Directory]::CreateDirectory($scratch) | Out-Null
$script:passed = 0
function Expect-Rejected([scriptblock]$Action, [string]$Pattern) {
    $rejected = $false
    try { & $Action | Out-Null } catch {
        if ($_ -notmatch $Pattern) { throw }
        $rejected = $true
    }
    if (-not $rejected) { throw 'Expected media safety rejection was absent' }
    $script:passed++
}
function Copy-Spec($Value) { return ($Value | ConvertTo-Json -Depth 12 | ConvertFrom-Json -AsHashtable) }
try {
    $selected = Get-MediaSpec $mediaRoot
    $spec = $selected.Spec
    $realPaths = Get-MediaPaths $mediaRoot $selected
    Assert-MediaSpec $spec
    $script:passed++
    foreach ($value in @('http://example.invalid/a', 'https://user:pass@example.invalid/a', 'https://example.invalid/a?secret=1')) {
        $bad = Copy-Spec $spec
        $bad.url = $value
        Expect-Rejected { Assert-MediaSpec $bad } 'HTTPS'
    }
    foreach ($edit in @(@('sha256', 'bad'), @('sizeBytes', $true), @('sizeBytes', 268435457),
        @('installPath', 'media/windows-amd64/../escape'), @('archiveRoot', '../escape'), @('vendorVersion', "bad`nversion"))) {
        $bad = Copy-Spec $spec
        $bad[$edit[0]] = $edit[1]
        Expect-Rejected { Assert-MediaSpec $bad } 'Invalid'
    }
    $bad = Copy-Spec $spec
    $bad.executables.ffmpeg.productionAllowed = $true
    Expect-Rejected { Assert-MediaSpec $bad } 'production policy'
    $bad = Copy-Spec $spec
    $bad.licenseFiles[0].path = $bad.archiveRoot + '/../outside'
    Expect-Rejected { Assert-MediaSpec $bad } 'license'

    # Complete offline entry point, with no production cache or network access.
    $fixture = Join-Path $scratch 'project'
    foreach ($name in @('scripts', 'tools', '.tools/downloads')) { [IO.Directory]::CreateDirectory((Join-Path $fixture $name)) | Out-Null }
    foreach ($name in @('bootstrap-media-tools.ps1', 'media-tools-lib.ps1', 'toolchain-lib.ps1')) {
        Copy-Item -LiteralPath (Join-Path $PSScriptRoot $name) -Destination (Join-Path $fixture 'scripts')
    }
    Copy-Item -LiteralPath (Join-Path $mediaRoot 'tools/manifest.json') -Destination (Join-Path $fixture 'tools/manifest.json')
    Expect-Rejected { & (Join-Path $fixture 'scripts/bootstrap-media-tools.ps1') -Offline } 'Offline media archive is missing'
    $fixturePaths = Get-MediaPaths $fixture $selected
    [IO.File]::WriteAllText($fixturePaths.Archive, 'untrusted archive bytes')
    Expect-Rejected { & (Join-Path $fixture 'scripts/bootstrap-media-tools.ps1') -Offline } 'SHA256 mismatch'
    if (Test-Path -LiteralPath $fixturePaths.Archive) { throw 'Bad cached archive was not removed' }
    if (Test-Path -LiteralPath $fixturePaths.Install) { throw 'Bad archive published an installation' }
    if (Test-Path -LiteralPath $fixturePaths.Record) { throw 'Bad archive published a record' }
    [IO.File]::WriteAllText($fixturePaths.Archive, 'test size mismatch')
    $fixtureManifest = Get-Content -LiteralPath (Join-Path $fixture 'tools/manifest.json') -Raw | ConvertFrom-Json -AsHashtable
    $fixtureManifest.mediaTools.platforms['windows-amd64'].sha256 = (Get-FileHash -LiteralPath $fixturePaths.Archive).Hash.ToLowerInvariant()
    $fixtureManifest | ConvertTo-Json -Depth 12 | Set-Content -LiteralPath (Join-Path $fixture 'tools/manifest.json') -Encoding utf8NoBOM
    Expect-Rejected { & (Join-Path $fixture 'scripts/bootstrap-media-tools.ps1') -Offline } 'size differs'
    if (Test-Path -LiteralPath $fixturePaths.Archive) { throw 'Incorrect-size cached archive was not removed' }
    Copy-Item -LiteralPath (Join-Path $mediaRoot 'tools/manifest.json') -Destination (Join-Path $fixture 'tools/manifest.json') -Force
    [IO.File]::WriteAllText($fixturePaths.Archive, 'untrusted archive bytes')
    $lockPath = [IO.Path]::ChangeExtension($fixturePaths.Record, '.lock')
    $heldLock = [IO.File]::Open($lockPath, [IO.FileMode]::OpenOrCreate, [IO.FileAccess]::ReadWrite, [IO.FileShare]::None)
    try {
        Expect-Rejected { & (Join-Path $fixture 'scripts/bootstrap-media-tools.ps1') -Offline } 'lock'
    } finally { $heldLock.Dispose() }
    # The same persistent lock file is usable once its handle is released.
    Expect-Rejected { & (Join-Path $fixture 'scripts/bootstrap-media-tools.ps1') -Offline } 'SHA256 mismatch'
    if (Test-Path -LiteralPath $fixturePaths.Archive) { throw 'Bad cached archive was not removed after lock reuse' }

    $record = @{ schemaVersion = 1; platform = 'windows-amd64'; vendorVersion = $spec.vendorVersion
        archiveSHA256 = $spec.sha256; executables = $spec.executables; licenseFiles = $spec.licenseFiles }
    $recordPath = Join-Path $scratch 'record.json'
    $record | ConvertTo-Json -Depth 8 | Set-Content -LiteralPath $recordPath -Encoding utf8NoBOM
    Assert-MediaRecord $spec 'windows-amd64' $recordPath
    $script:passed++
    $record.executables = Copy-Spec $spec.executables
    $record.executables.ffprobe.sha256 = '0' * 64
    $record | ConvertTo-Json -Depth 8 | Set-Content -LiteralPath $recordPath -Encoding utf8NoBOM
    Expect-Rejected { Assert-MediaRecord $spec 'windows-amd64' $recordPath } 'record'

    # Known files with an altered manifest must fail before version execution.
    $bad = Copy-Spec $spec
    $bad.executables.ffprobe.sha256 = '0' * 64
    Expect-Rejected { Assert-MediaFiles $mediaRoot $bad $realPaths.Install @('ffprobe') } 'SHA256 mismatch'
    $bad = Copy-Spec $spec
    $bad.licenseFiles[0].path = $spec.archiveRoot + '/missing-license'
    Expect-Rejected { Assert-MediaFiles $mediaRoot $bad $realPaths.Install @('ffprobe') } 'find file|does not exist'
    $bad = Copy-Spec $spec
    $bad.vendorVersion = 'incorrect-version'
    Expect-Rejected { Assert-MediaVersion $mediaRoot $bad $realPaths.Install 'ffprobe' } 'version differs'
    Test-MediaInstallation $mediaRoot
    $script:passed++

    foreach ($stream in @('Out', 'Error')) {
        $info = [Diagnostics.ProcessStartInfo]::new()
        $info.FileName = (Get-Process -Id $PID).Path
        $info.UseShellExecute = $false
        $info.CreateNoWindow = $true
        $info.RedirectStandardOutput = $true
        $info.RedirectStandardError = $true
        foreach ($argument in @('-NoProfile', '-Command', "[Console]::$stream.Write(('x' * 100000)); [Threading.Thread]::Sleep(20000)")) { $info.ArgumentList.Add($argument) }
        Set-MediaChildEnvironment $mediaRoot $info
        Expect-Rejected { [Jelee.MediaVersionReader]::Run($info, 5000, 65536) } 'output exceeds'
    }
    $info.ArgumentList.Clear()
    foreach ($argument in @('-NoProfile', '-Command', '[Threading.Thread]::Sleep(20000)')) { $info.ArgumentList.Add($argument) }
    Expect-Rejected { [Jelee.MediaVersionReader]::Run($info, 100, 65536) } 'timed out'
    $info.FileName = Join-Path $scratch 'missing-executable.exe'
    Expect-Rejected { [Jelee.MediaVersionReader]::Run($info, 100, 65536) } 'missing-executable'
    $info.Environment['LD_PRELOAD'] = 'untrusted'
    $info.Environment['LD_LIBRARY_PATH'] = 'untrusted'
    $info.Environment['FFREPORT'] = 'untrusted'
    Set-MediaChildEnvironment $mediaRoot $info
    foreach ($key in @('LD_PRELOAD', 'LD_LIBRARY_PATH', 'FFREPORT')) {
        if ($info.Environment.ContainsKey($key)) { throw 'Untrusted media environment was inherited' }
    }
    $script:passed++

    # The child wrapper must consume native -v/-f flags and suppress ambient reports.
    $priorReport = $env:FFREPORT
    $reportFile = Assert-LocalPath $mediaRoot (Join-Path $scratch 'unexpected-report.log')
    try {
        $env:FFREPORT = 'file=unexpected-report.log:level=48'
        Push-Location $scratch
        try {
            $output = & pwsh -NoProfile -File (Join-Path $PSScriptRoot 'run-media-tool.ps1') -Tool ffprobe -v error -f lavfi -i 'color=size=16x16:rate=1' -show_entries stream=width,height -of json
            if ($LASTEXITCODE -ne 0) { throw 'Wrapper native argument forwarding failed' }
        } finally { Pop-Location }
        $result = $output -join "`n" | ConvertFrom-Json
        if ($result.streams[0].width -ne 16 -or $result.streams[0].height -ne 16) { throw 'Wrapper returned unexpected probe result' }
        if (Test-Path -LiteralPath $reportFile) { throw 'Wrapper inherited FFREPORT' }
        $script:passed++
    } finally { $env:FFREPORT = $priorReport }
    Write-Host "PASS: $script:passed media safety cases (manifest, offline, checksum, lock, record, license, version, bounded output, timeout, arguments, environment)"
} finally { Remove-LocalTree $mediaRoot $scratch }
