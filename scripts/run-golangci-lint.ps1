#requires -Version 7.2
# Run the manifest-pinned golangci-lint with the pinned Go toolchain first on
# PATH. Arguments are forwarded unchanged; a non-zero exit code throws.
$lintArgs = $args
. "$PSScriptRoot/toolchain-lib.ps1"
$root = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..'))
$selected = Get-GolangciSpec $root
$exe = Assert-LocalPath $root (Join-Path $root ".tools/$($selected.Spec.installPath)/$($selected.Spec.executable)")
if (-not (Test-Path -LiteralPath $exe)) { throw 'Run scripts/bootstrap-tools.ps1 first' }
$code = Invoke-WithGoEnvironment $root $exe $lintArgs
$global:LASTEXITCODE = $code
if ($code -ne 0) { throw "golangci-lint exited with code $code" }
