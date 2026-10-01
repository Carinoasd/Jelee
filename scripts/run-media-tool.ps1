#requires -Version 7.2
# No param block: FFmpeg flags must never bind to PowerShell common parameters.
. "$PSScriptRoot/media-tools-lib.ps1"
if ($args.Count -lt 2 -or $args[0] -cne '-Tool' -or $args[1] -cnotin @('ffmpeg', 'ffprobe')) {
    throw 'Usage: run-media-tool.ps1 -Tool ffmpeg|ffprobe [native arguments]'
}
$mediaName = $args[1]
$mediaArguments = @($args | Select-Object -Skip 2)
$mediaRoot = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..'))
$selected = Get-MediaSpec $mediaRoot
$paths = Get-MediaPaths $mediaRoot $selected
Assert-MediaRecord $selected.Spec $selected.Platform $paths.Record
Assert-MediaFiles $mediaRoot $selected.Spec $paths.Install @($mediaName)
Assert-MediaVersion $mediaRoot $selected.Spec $paths.Install $mediaName
$mediaExecutable = Assert-LocalPath $mediaRoot (Join-Path $paths.Install $selected.Spec.executables[$mediaName].path)
$info = [Diagnostics.ProcessStartInfo]::new()
$info.FileName = $mediaExecutable
$info.UseShellExecute = $false
foreach ($argument in $mediaArguments) { $info.ArgumentList.Add([string]$argument) }
Set-MediaChildEnvironment $mediaRoot $info
$process = [Diagnostics.Process]::new()
$process.StartInfo = $info
try {
    if (-not $process.Start()) { throw 'Media process did not start' }
    $process.WaitForExit()
    $mediaExit = $process.ExitCode
} finally {
    $process.Dispose()
}
exit $mediaExit
