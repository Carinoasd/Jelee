#requires -Version 7.2
# Forward all arguments without binding flags such as -v to PowerShell parameters.
$goArgs = $args
. "$PSScriptRoot/toolchain-lib.ps1"
$root = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..'))
$selected = Get-GoSpec $root
$goRoot = Assert-LocalPath $root (Join-Path $root ".tools/$($selected.Spec.installPath)/go")
$exe = Assert-LocalPath $root (Join-Path $goRoot 'bin/go.exe')
if (-not (Test-Path -LiteralPath $exe)) { throw 'Run scripts/bootstrap-tools.ps1 first' }
$cache = Assert-LocalPath $root (Join-Path $root '.tools/cache')
$settings = @{
    GOTOOLCHAIN = 'local'; GOENV = 'off'; GOROOT = $goRoot
    GOCACHE = "$cache/go-build"; GOPATH = "$cache/gopath"; GOMODCACHE = "$cache/gomod"
    GOTMPDIR = "$cache/tmp"; TMP = "$cache/tmp"; TEMP = "$cache/tmp"; TMPDIR = "$cache/tmp"
    APPDATA = "$cache/config"; LOCALAPPDATA = "$cache/local"
    XDG_CONFIG_HOME = "$cache/config"
}
$saved = @{}
foreach ($name in $settings.Keys) { $saved[$name] = [Environment]::GetEnvironmentVariable($name, 'Process') }
try {
    foreach ($name in $settings.Keys) { [Environment]::SetEnvironmentVariable($name, $settings[$name], 'Process') }
    foreach ($name in @('GOCACHE','GOPATH','GOMODCACHE','GOTMPDIR','APPDATA','LOCALAPPDATA')) {
        $dir = Assert-LocalPath $root $settings[$name]
        [IO.Directory]::CreateDirectory($dir) | Out-Null
    }
    & $exe @goArgs
    $script:goExitCode = $LASTEXITCODE
} finally {
    foreach ($name in $saved.Keys) { [Environment]::SetEnvironmentVariable($name, $saved[$name], 'Process') }
}
$global:LASTEXITCODE = $script:goExitCode
if ($script:goExitCode -ne 0) { throw "go exited with code $script:goExitCode" }
