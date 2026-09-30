#requires -Version 7.2
[CmdletBinding()]
param()
. "$PSScriptRoot/toolchain-lib.ps1"
$root = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..'))
Push-Location $root
try {
    $binaryDirectory = Assert-LocalPath $root (Join-Path $root '.tools/cache/fixture-bin')
    [IO.Directory]::CreateDirectory($binaryDirectory) | Out-Null
    $binaryPath = Assert-LocalPath $root (Join-Path $binaryDirectory 'gen-fixtures.exe')
    & "$PSScriptRoot/run-go.ps1" build -trimpath -tags jelee_fixture_tools -o $binaryPath ./tools/gen-fixtures
    & $binaryPath
    if ($LASTEXITCODE -ne 0) { throw 'fixture generation failed' }
} finally { Pop-Location }
