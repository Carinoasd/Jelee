#requires -Version 7.2
. "$PSScriptRoot/toolchain-lib.ps1"
$root = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..'))
$scratch = Assert-LocalPath $root (Join-Path $root ('.tools/bootstrap-tests-' + [Guid]::NewGuid().ToString('N')))
[IO.Directory]::CreateDirectory($scratch) | Out-Null
$passed = 0
function Make-TestZip([string]$Name, [string]$EntryName, [int]$Attributes = 0) {
    $path = Join-Path $scratch $Name
    $zip = [IO.Compression.ZipFile]::Open($path, [IO.Compression.ZipArchiveMode]::Create)
    try {
        $entry = $zip.CreateEntry($EntryName)
        $entry.ExternalAttributes = $Attributes
        $writer = [IO.StreamWriter]::new($entry.Open())
        try { $writer.Write('safe content') } finally { $writer.Dispose() }
    } finally { $zip.Dispose() }
    return $path
}
try {
    $safe = Make-TestZip 'valid.zip' 'go/bin/tool.exe'
    $dest = Join-Path $scratch 'valid'
    [IO.Directory]::CreateDirectory($dest) | Out-Null
    Expand-SafeZip $safe $dest
    if ((Get-Content -LiteralPath (Join-Path $dest 'go/bin/tool.exe') -Raw) -ne 'safe content') { throw 'Valid extraction failed' }
    $passed++
    foreach ($bad in @('../escape.txt','/absolute.txt','C:/absolute.txt','go/../../escape.txt','go/./ambiguous.txt','go/.. /escape.txt','go/NUL.txt')) {
        $zip = Make-TestZip ('bad-' + $passed + '.zip') $bad
        $dest = Join-Path $scratch ('bad-' + $passed)
        [IO.Directory]::CreateDirectory($dest) | Out-Null
        $rejected = $false
        try { Expand-SafeZip $zip $dest } catch { $rejected = $true }
        if (-not $rejected) { throw "Unsafe entry accepted: $bad" }
        $passed++
    }
    $link = Make-TestZip 'link.zip' 'go/link' (-1610612736)
    $dest = Join-Path $scratch 'link'
    [IO.Directory]::CreateDirectory($dest) | Out-Null
    $rejected = $false
    try { Expand-SafeZip $link $dest } catch { $rejected = $true }
    if (-not $rejected) { throw 'Symlink archive entry accepted' }
    $passed++
    $rejected = $false
    try { Assert-ArchiveHash $safe ('0' * 64) } catch { $rejected = $true }
    if (-not $rejected) { throw 'Wrong checksum accepted' }
    $passed++
    $rejected = $false
    try { Assert-LocalPath $scratch (Join-Path $scratch '../escape') | Out-Null } catch { $rejected = $true }
    if (-not $rejected) { throw 'Boundary escape accepted' }
    $passed++
    # Exercise the complete offline bootstrap failure, including cache cleanup.
    $fixture = Join-Path $scratch 'checksum-project'
    foreach ($name in @('scripts','tools','.tools/downloads')) { [IO.Directory]::CreateDirectory((Join-Path $fixture $name)) | Out-Null }
    Copy-Item -LiteralPath "$PSScriptRoot/bootstrap-tools.ps1", "$PSScriptRoot/toolchain-lib.ps1" -Destination (Join-Path $fixture 'scripts')
    $manifest = Get-Content -LiteralPath (Join-Path $root 'tools/manifest.json') -Raw | ConvertFrom-Json
    $platform = (Get-GoSpec $root).Platform
    $spec = $manifest.tools[0].platforms.$platform
    $spec.sha256 = '0' * 64
    $manifest | ConvertTo-Json -Depth 10 | Set-Content -LiteralPath (Join-Path $fixture 'tools/manifest.json') -Encoding utf8NoBOM
    $badCache = Join-Path $fixture ('.tools/downloads/' + [IO.Path]::GetFileName(([Uri]$spec.url).AbsolutePath))
    [IO.File]::WriteAllText($badCache, 'untrusted archive bytes')
    $rejected = $false
    try { & (Join-Path $fixture 'scripts/bootstrap-tools.ps1') -Offline } catch {
        if ($_ -notmatch 'SHA256 mismatch') { throw }
        $rejected = $true
    }
    if (-not $rejected -or (Test-Path -LiteralPath $badCache)) { throw 'Bootstrap did not reject and remove bad archive' }
    $passed++
    # golangci-lint manifest entry: accepted as pinned, rejected once tampered.
    $lint = Get-GolangciSpec $root
    if ($lint.Spec.sha256 -notmatch '^[a-f0-9]{64}$') { throw 'golangci-lint spec lacks a SHA256' }
    $lintFixture = Join-Path $scratch 'golangci-project'
    [IO.Directory]::CreateDirectory((Join-Path $lintFixture 'tools')) | Out-Null
    $lintManifest = Get-Content -LiteralPath (Join-Path $root 'tools/manifest.json') -Raw | ConvertFrom-Json
    $lintEntry = @($lintManifest.tools | Where-Object name -eq 'golangci-lint')[0]
    $lintEntry.platforms.($lint.Platform).installPath = '../escape'
    $lintManifest | ConvertTo-Json -Depth 10 | Set-Content -LiteralPath (Join-Path $lintFixture 'tools/manifest.json') -Encoding utf8NoBOM
    $rejected = $false
    try { Get-GolangciSpec $lintFixture | Out-Null } catch { $rejected = $true }
    if (-not $rejected) { throw 'Tampered golangci-lint layout accepted' }
    $passed++
    Write-Host "PASS: $passed bootstrap security cases (valid ZIP, traversal, absolute path, link, checksum, boundary, golangci-lint layout)"
} finally { Remove-LocalTree $root $scratch }
