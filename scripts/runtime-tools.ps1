#Requires -Version 7.2
param(
    [ValidateSet('bootstrap', 'inspect', 'verify', 'sources')]
    [string]$Command = 'bootstrap',
    [switch]$Offline,
    [ValidatePattern('^[A-Za-z0-9._-]{1,64}$')]
    [string]$Distribution = 'Ubuntu'
)
$ErrorActionPreference = 'Stop'
$taskScript = Join-Path $PSScriptRoot 'runtime-tools.py'
$taskArguments = @($Command)
if ($Offline) { $taskArguments += '--offline' }
if ($IsWindows) {
    # Runtime is Linux amd64 only. Use an existing explicit WSL distro; never
    # install a distro, Python, packages, or a native Windows fallback.
    $taskLinuxPath = & wsl.exe -d $Distribution --exec wslpath -a $taskScript
    if ($LASTEXITCODE -ne 0 -or -not $taskLinuxPath -or $taskLinuxPath.Count -gt 1) {
        throw 'runtime-tools requires an existing WSL distro with Python 3'
    }
    & wsl.exe -d $Distribution --exec python3 -B $taskLinuxPath @taskArguments
} else {
    & python3 -B $taskScript @taskArguments
}
exit $LASTEXITCODE
