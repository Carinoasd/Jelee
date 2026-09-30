#requires -Version 7.2
. "$PSScriptRoot/toolchain-lib.ps1"

if (-not ('Jelee.MediaVersionReader' -as [type])) {
    Add-Type -TypeDefinition @'
using System;
using System.Diagnostics;
using System.IO;
using System.Text;
using System.Threading.Tasks;
namespace Jelee {
    public static class MediaVersionReader {
        static async Task<byte[]> ReadBounded(Stream stream, Process process, int limit) {
            using var output = new MemoryStream();
            byte[] buffer = new byte[4096];
            while (true) {
                int count = await stream.ReadAsync(buffer, 0, Math.Min(buffer.Length, limit - (int)output.Length + 1));
                if (count == 0) return output.ToArray();
                if (output.Length + count > limit) {
                    try { if (!process.HasExited) process.Kill(true); } catch (InvalidOperationException) { }
                    throw new InvalidDataException("Media version output exceeds limit");
                }
                output.Write(buffer, 0, count);
            }
        }
        public static string Run(ProcessStartInfo info, int timeout, int limit) {
            using var process = new Process { StartInfo = info };
            if (!process.Start()) throw new InvalidOperationException("Media version process did not start");
            var stdout = ReadBounded(process.StandardOutput.BaseStream, process, limit);
            var stderr = ReadBounded(process.StandardError.BaseStream, process, limit);
            var elapsed = Stopwatch.StartNew();
            try {
                if (!process.WaitForExit(timeout)) throw new TimeoutException("Media version process timed out");
                int remaining = timeout - (int)elapsed.ElapsedMilliseconds;
                if (remaining <= 0 || !Task.WaitAll(new Task[] { stdout, stderr }, remaining))
                    throw new TimeoutException("Media version process timed out");
                if (process.ExitCode != 0) throw new InvalidOperationException("Media version process failed");
                return Encoding.UTF8.GetString(stdout.GetAwaiter().GetResult());
            } finally {
                if (!process.HasExited) process.Kill(true);
                process.WaitForExit();
                process.StandardOutput.Close();
                process.StandardError.Close();
                // Closing the bounded streams releases any pending read after a timeout.
                try { Task.WaitAll(new Task[] { stdout, stderr }); } catch (AggregateException) { }
            }
        }
    }
}
'@
}

function Assert-MediaHTTPS([string]$Value) {
    $uri = $null
    if (-not [Uri]::TryCreate($Value, [UriKind]::Absolute, [ref]$uri) -or $uri.Scheme -cne 'https' -or
        -not $uri.Host -or $uri.UserInfo -or $uri.Query -or $uri.Fragment) {
        throw 'Media sources and mirrors must use credential-free HTTPS'
    }
}

function Get-MediaSpec([string]$Root) {
    if (-not $IsWindows -or [Runtime.InteropServices.RuntimeInformation]::OSArchitecture.ToString() -ne 'X64') {
        throw 'PowerShell media bootstrap supports Windows amd64 only; use bootstrap-media-tools on Linux amd64'
    }
    $manifestPath = Assert-LocalPath $Root (Join-Path $Root 'tools/manifest.json')
    $manifest = Get-Content -LiteralPath $manifestPath -Raw | ConvertFrom-Json -AsHashtable
    if ($manifest.schemaVersion -ne 1 -or $manifest.mediaTools.schemaVersion -ne 1 -or $manifest.mediaTools.optional -cne $true) {
        throw 'Invalid optional media manifest'
    }
    $spec = $manifest.mediaTools.platforms['windows-amd64']
    Assert-MediaSpec $spec
    return @{ Spec = $spec; Platform = 'windows-amd64' }
}

function Assert-MediaSpec($Spec) {
    Assert-MediaHTTPS $Spec.url
    if ($Spec.sha256 -cnotmatch '^[a-f0-9]{64}$' -or $Spec.archive -cne 'zip' -or
        $Spec.sizeBytes -isnot [long] -and $Spec.sizeBytes -isnot [int] -or
        $Spec.sizeBytes -le 0 -or $Spec.sizeBytes -gt 268435456 -or
        $Spec.vendorVersion -cnotmatch '^[A-Za-z0-9._+-]{1,100}$' -or
        $Spec.archiveRoot -cnotmatch '^[A-Za-z0-9._+-]{1,100}$' -or
        $Spec.installPath -cnotmatch '^media/windows-amd64/[A-Za-z0-9._+-]+$') {
        throw 'Invalid media archive, version or installation layout'
    }
    if ($Spec.executables.Count -ne 2 -or -not $Spec.executables.Contains('ffmpeg') -or -not $Spec.executables.Contains('ffprobe')) {
        throw 'Media manifest must declare ffmpeg and ffprobe'
    }
    foreach ($name in @('ffmpeg', 'ffprobe')) {
        $entry = $Spec.executables[$name]
        if ($entry.path -cne "$($Spec.archiveRoot)/bin/$name.exe" -or $entry.sha256 -cnotmatch '^[a-f0-9]{64}$' -or
            $entry.productionAllowed -isnot [bool] -or $entry.productionAllowed -ne ($name -eq 'ffprobe')) {
            throw 'Invalid executable hash, layout or production policy'
        }
    }
    if ($Spec.licenseFiles.Count -lt 1 -or $Spec.licenseFiles.Count -gt 16) { throw 'License files must be declared' }
    foreach ($entry in $Spec.licenseFiles) {
        if (-not $entry.path.StartsWith($Spec.archiveRoot + '/', [StringComparison]::Ordinal) -or
            $entry.path.Contains('\') -or $entry.path.Contains(':') -or
            @($entry.path.Split('/') | Where-Object { $_ -in @('', '.', '..') }).Count -or
            $entry.sha256 -cnotmatch '^[a-f0-9]{64}$') { throw 'Invalid license hash or layout' }
    }
}

function Get-MediaPaths([string]$Root, $Selected) {
    $spec = $Selected.Spec
    return @{
        Archive = Assert-LocalPath $Root (Join-Path $Root ".tools/downloads/$([IO.Path]::GetFileName(([Uri]$spec.url).AbsolutePath))")
        Install = Assert-LocalPath $Root (Join-Path $Root ".tools/$($spec.installPath)")
        Record = Assert-LocalPath $Root (Join-Path $Root ".tools/media-installed/$($Selected.Platform).json")
    }
}

function Get-MediaEnvironment([string]$Root) {
    $values = @{}
    foreach ($key in @('TMPDIR', 'TMP', 'TEMP', 'XDG_CONFIG_HOME')) {
        $leaf = if ($key -eq 'XDG_CONFIG_HOME') { 'config' } else { 'tmp' }
        $directory = Assert-LocalPath $Root (Join-Path $Root ".tools/cache/media/$leaf")
        [IO.Directory]::CreateDirectory($directory) | Out-Null
        $values[$key] = $directory
    }
    return $values
}

function Set-MediaChildEnvironment([string]$Root, [Diagnostics.ProcessStartInfo]$Info) {
    foreach ($entry in (Get-MediaEnvironment $Root).GetEnumerator()) { $Info.Environment[$entry.Key] = $entry.Value }
    foreach ($key in @($Info.Environment.Keys)) {
        if ($key -eq 'FFREPORT' -or $key.StartsWith('LD_', [StringComparison]::OrdinalIgnoreCase)) {
            $Info.Environment.Remove($key) | Out-Null
        }
    }
}

function Assert-MediaFiles([string]$Root, $Spec, [string]$Install, [string[]]$Names = @('ffmpeg', 'ffprobe')) {
    foreach ($name in $Names) {
        $entry = $Spec.executables[$name]
        $file = Assert-LocalPath $Root (Join-Path $Install $entry.path)
        Assert-ArchiveHash $file $entry.sha256
    }
    foreach ($entry in $Spec.licenseFiles) {
        Assert-ArchiveHash (Assert-LocalPath $Root (Join-Path $Install $entry.path)) $entry.sha256
    }
}

function Assert-MediaVersion([string]$Root, $Spec, [string]$Install, [string]$Name) {
    $info = [Diagnostics.ProcessStartInfo]::new()
    $info.FileName = Assert-LocalPath $Root (Join-Path $Install $Spec.executables[$Name].path)
    $info.ArgumentList.Add('-version')
    $info.UseShellExecute = $false
    $info.CreateNoWindow = $true
    $info.RedirectStandardOutput = $true
    $info.RedirectStandardError = $true
    Set-MediaChildEnvironment $Root $info
    $stdout = [Jelee.MediaVersionReader]::Run($info, 10000, 65536)
    if (-not $stdout.StartsWith("$Name version $($Spec.vendorVersion) ", [StringComparison]::Ordinal)) {
        throw 'Media executable version differs from manifest'
    }
}

function Assert-MediaRecord($Spec, [string]$Platform, [string]$Record) {
    $installed = Get-Content -LiteralPath $Record -Raw | ConvertFrom-Json -AsHashtable
    if ($installed.schemaVersion -ne 1 -or $installed.platform -cne $Platform -or
        $installed.vendorVersion -cne $Spec.vendorVersion -or $installed.archiveSHA256 -cne $Spec.sha256 -or
        $installed.executables.Count -ne 2 -or $installed.licenseFiles.Count -ne $Spec.licenseFiles.Count) {
        throw 'Media installation record differs from manifest; bootstrap again'
    }
    foreach ($name in @('ffmpeg', 'ffprobe')) {
        foreach ($field in @('path', 'sha256', 'productionAllowed')) {
            if ($installed.executables[$name][$field] -cne $Spec.executables[$name][$field]) {
                throw 'Media installation record differs from manifest; bootstrap again'
            }
        }
    }
    for ($i = 0; $i -lt $Spec.licenseFiles.Count; $i++) {
        foreach ($field in @('path', 'sha256')) {
            if ($installed.licenseFiles[$i][$field] -cne $Spec.licenseFiles[$i][$field]) {
                throw 'Media license record differs from manifest; bootstrap again'
            }
        }
    }
}

function Test-MediaInstallation([string]$Root) {
    $selected = Get-MediaSpec $Root
    $paths = Get-MediaPaths $Root $selected
    Assert-ArchiveHash $paths.Archive $selected.Spec.sha256
    if ((Get-Item -LiteralPath $paths.Archive).Length -ne $selected.Spec.sizeBytes) { throw 'Media archive size differs from manifest' }
    Assert-MediaRecord $selected.Spec $selected.Platform $paths.Record
    Assert-MediaFiles $Root $selected.Spec $paths.Install
    foreach ($name in @('ffmpeg', 'ffprobe')) { Assert-MediaVersion $Root $selected.Spec $paths.Install $name }
    Write-Host "Verified media tools $($selected.Platform) $($selected.Spec.vendorVersion); archive, executable and license SHA256 match"
}
