#requires -Version 7.2
. "$PSScriptRoot/toolchain-lib.ps1"
$root = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..'))
$selected = Get-GoSpec $root
$spec = $selected.Spec
$archiveName = [IO.Path]::GetFileName(([Uri]$spec.url).AbsolutePath)
$archive = Assert-LocalPath $root (Join-Path $root ".tools/downloads/$archiveName")
Assert-ArchiveHash $archive $spec.sha256
$recordPath = Assert-LocalPath $root (Join-Path $root '.tools/.installed.json')
$installed = Get-Content -LiteralPath $recordPath -Raw | ConvertFrom-Json
$record = $installed.platforms.($selected.Platform)
if ($record.version -ne $selected.Tool.version -or $record.platform -ne $selected.Platform -or $record.archiveSHA256 -ne $spec.sha256) {
    throw 'Installed record does not match manifest; bootstrap again'
}
$exe = Assert-LocalPath $root (Join-Path $root ".tools/$($spec.installPath)/$($spec.executable)")
Assert-ArchiveHash $exe $record.executableSHA256
Assert-GoExecutablesFromZip $archive (Join-Path $root ".tools/$($spec.installPath)")
$actual = & "$PSScriptRoot/run-go.ps1" version
if ($actual -ne "go version go$($selected.Tool.version) windows/$($selected.Platform.Split('-')[1])") {
    throw "Unexpected toolchain version: $actual"
}
$goMod = Join-Path $root 'go.mod'
if ((Test-Path -LiteralPath $goMod) -and (Get-Content -LiteralPath $goMod -Raw) -notmatch "(?m)^go $([regex]::Escape($selected.Tool.version))$") {
    throw 'go.mod version differs from tools/manifest.json'
}
Write-Host "Verified $actual; archive SHA256 and installed binary match"
