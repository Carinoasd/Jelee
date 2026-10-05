#requires -Version 7.2
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

function Assert-LocalPath([string]$Root, [string]$Path) {
    $rootFull = [IO.Path]::GetFullPath($Root).TrimEnd([IO.Path]::DirectorySeparatorChar)
    $full = [IO.Path]::GetFullPath($Path)
    $comparison = if ($IsWindows) { [StringComparison]::OrdinalIgnoreCase } else { [StringComparison]::Ordinal }
    if (-not $full.StartsWith($rootFull + [IO.Path]::DirectorySeparatorChar, $comparison)) {
        throw "Path must remain below project directory: $full"
    }
    $current = $full
    while ($current -and $current.Length -ge $rootFull.Length) {
        if (Test-Path -LiteralPath $current) {
            if ((Get-Item -LiteralPath $current -Force).Attributes -band [IO.FileAttributes]::ReparsePoint) {
                throw "Refusing linked directory or file: $current"
            }
        }
        $current = [IO.Path]::GetDirectoryName($current)
    }
    return $full
}

function Get-GoSpec([string]$Root) {
    $manifest = Get-Content -LiteralPath (Join-Path $Root 'tools/manifest.json') -Raw | ConvertFrom-Json
    if ($manifest.schemaVersion -ne 1) { throw 'Unsupported manifest schema' }
    $tool = @($manifest.tools | Where-Object name -eq 'go')
    if ($tool.Count -ne 1 -or $tool[0].version -notmatch '^\d+\.\d+\.\d+$') { throw 'Invalid Go manifest entry' }
    $arch = switch ([Runtime.InteropServices.RuntimeInformation]::OSArchitecture.ToString()) {
        'X64' { 'amd64' }
        'Arm64' { 'arm64' }
        default { throw 'Supported architectures: amd64 and arm64' }
    }
    if (-not $IsWindows) { throw 'Use scripts/bootstrap-tools on Linux' }
    $platform = "windows-$arch"
    $spec = $tool[0].platforms.$platform
    if ($spec.url -notmatch '^https://' -or $spec.sha256 -notmatch '^[a-f0-9]{64}$' -or $spec.archive -ne 'zip') {
        throw 'Invalid HTTPS source, SHA256, or archive type'
    }
    if ($spec.installPath -ne "go/$($tool[0].version)/$platform" -or $spec.executable -ne 'go/bin/go.exe') {
        throw 'Unexpected tool installation layout'
    }
    return @{ Tool = $tool[0]; Spec = $spec; Platform = $platform }
}

function Assert-ArchiveHash([string]$Path, [string]$Expected) {
    if ((Get-FileHash -LiteralPath $Path -Algorithm SHA256).Hash.ToLowerInvariant() -ne $Expected) {
        throw "SHA256 mismatch for $([IO.Path]::GetFileName($Path))"
    }
}

function Expand-SafeZip([string]$Archive, [string]$Destination) {
    Add-Type -AssemblyName System.IO.Compression
    if (@(Get-ChildItem -LiteralPath $Destination -Force).Count) { throw 'Extraction staging directory must be empty' }
    $destinationFull = [IO.Path]::GetFullPath($Destination).TrimEnd([IO.Path]::DirectorySeparatorChar)
    $checkedDirs = [Collections.Generic.HashSet[string]]::new([StringComparer]::OrdinalIgnoreCase)
    $checkedDirs.Add($destinationFull) | Out-Null
    function New-SafeDirectory([string]$Directory) {
        if ($checkedDirs.Contains($Directory)) { return }
        New-SafeDirectory ([IO.Path]::GetDirectoryName($Directory))
        # Validate an existing directory on first use. Archive entries can never
        # create links, and the installer alone populates the fresh staging tree.
        if (Test-Path -LiteralPath $Directory) {
            if ((Get-Item -LiteralPath $Directory -Force).Attributes -band [IO.FileAttributes]::ReparsePoint) { throw 'Linked extraction directory' }
        }
        [IO.Directory]::CreateDirectory($Directory) | Out-Null
        $checkedDirs.Add($Directory) | Out-Null
    }
    $zip = [IO.Compression.ZipFile]::OpenRead($Archive)
    try {
        [long]$total = 0
        if ($zip.Entries.Count -gt 100000) { throw 'Archive has too many entries' }
        foreach ($entry in $zip.Entries) {
            $name = $entry.FullName.Replace('\', '/')
            if ($name.StartsWith('/') -or $name.Contains(':') -or @($name.Split('/') | Where-Object { $_ -eq '..' -or $_ -eq '.' }).Count) {
                throw "Unsafe archive path: $name"
            }
            foreach ($part in $name.Split('/')) {
                if ($part -and ($part -ne $part.TrimEnd(' ', '.') -or $part -match '^(?i:CON|PRN|AUX|NUL|COM[1-9]|LPT[1-9])(?:\.|$)')) {
                    throw "Unsafe Windows archive name: $name"
                }
            }
            $kind = ($entry.ExternalAttributes -shr 16) -band 0xF000
            if ($kind -notin @(0, 0x4000, 0x8000)) { throw "Archive links and special files are forbidden: $name" }
            $total += $entry.Length
            if ($total -gt 2147483648) { throw 'Archive expanded size exceeds 2 GiB' }
            $target = [IO.Path]::GetFullPath((Join-Path $destinationFull $name)).TrimEnd([IO.Path]::DirectorySeparatorChar)
            if (-not $target.StartsWith($destinationFull + [IO.Path]::DirectorySeparatorChar, [StringComparison]::OrdinalIgnoreCase)) { throw 'Archive path escaped staging' }
            if ($name.EndsWith('/')) {
                New-SafeDirectory $target
            } else {
                New-SafeDirectory ([IO.Path]::GetDirectoryName($target))
                $inputStream = $entry.Open()
                try {
                    $outputStream = [IO.File]::Open($target, [IO.FileMode]::CreateNew, [IO.FileAccess]::Write)
                    try { $inputStream.CopyTo($outputStream) } finally { $outputStream.Dispose() }
                } finally { $inputStream.Dispose() }
            }
        }
    } finally { $zip.Dispose() }
}

function Assert-GoExecutablesFromZip([string]$Archive, [string]$InstallDir) {
    $zip = [IO.Compression.ZipFile]::OpenRead($Archive)
    try {
        foreach ($name in @('go/bin/go.exe','go/bin/gofmt.exe')) {
            $entry = $zip.GetEntry($name)
            if ($null -eq $entry) { throw "Archive executable absent: $name" }
            $stream = $entry.Open()
            $hash = [Security.Cryptography.SHA256]::Create()
            try { $expected = [Convert]::ToHexString($hash.ComputeHash($stream)).ToLowerInvariant() }
            finally { $hash.Dispose(); $stream.Dispose() }
            $file = Assert-LocalPath $InstallDir (Join-Path $InstallDir $name)
            Assert-ArchiveHash $file $expected
        }
    } finally { $zip.Dispose() }
}

function Remove-LocalTree([string]$Root, [string]$Path) {
    $resolved = Assert-LocalPath $Root $Path
    if (Test-Path -LiteralPath $resolved) {
        # Reject linked descendants before recursive removal as well as the target.
        foreach ($item in Get-ChildItem -LiteralPath $resolved -Force -Recurse) {
            if ($item.Attributes -band [IO.FileAttributes]::ReparsePoint) { throw "Refusing cleanup of linked tree: $resolved" }
        }
        Remove-Item -LiteralPath $resolved -Recurse -Force
    }
}

function Get-GolangciSpec([string]$Root) {
    $manifest = Get-Content -LiteralPath (Join-Path $Root 'tools/manifest.json') -Raw | ConvertFrom-Json
    if ($manifest.schemaVersion -ne 1) { throw 'Unsupported manifest schema' }
    $tool = @($manifest.tools | Where-Object name -eq 'golangci-lint')
    if ($tool.Count -ne 1 -or $tool[0].version -notmatch '^\d+\.\d+\.\d+$') { throw 'Invalid golangci-lint manifest entry' }
    $arch = switch ([Runtime.InteropServices.RuntimeInformation]::OSArchitecture.ToString()) {
        'X64' { 'amd64' }
        'Arm64' { 'arm64' }
        default { throw 'Supported architectures: amd64 and arm64' }
    }
    if (-not $IsWindows) { throw 'Use scripts/bootstrap-tools on Linux' }
    $platform = "windows-$arch"
    $version = $tool[0].version
    $spec = $tool[0].platforms.$platform
    $archiveRoot = "golangci-lint-$version-$platform"
    if ($spec.url -ne "https://github.com/golangci/golangci-lint/releases/download/v$version/$archiveRoot.zip" -or
        $spec.sha256 -notmatch '^[a-f0-9]{64}$' -or $spec.archive -ne 'zip' -or $spec.archiveRoot -ne $archiveRoot) {
        throw 'Invalid golangci-lint HTTPS source, SHA256, or archive type'
    }
    if ($spec.installPath -ne "golangci-lint/$version/$platform" -or $spec.executable -ne "$archiveRoot/golangci-lint.exe" -or
        $spec.licenseFile -ne "$archiveRoot/LICENSE" -or $tool[0].cache -ne 'cache/golangci-lint') {
        throw 'Unexpected golangci-lint installation layout'
    }
    return @{ Tool = $tool[0]; Spec = $spec; Platform = $platform }
}

# Compare installed files with the same entries of the verified archive.
function Assert-ZipEntryHashes([string]$Archive, [string]$InstallDir, [string[]]$Names) {
    Add-Type -AssemblyName System.IO.Compression
    $zip = [IO.Compression.ZipFile]::OpenRead($Archive)
    try {
        foreach ($name in $Names) {
            $entry = $zip.GetEntry($name)
            if ($null -eq $entry) { throw "Archive file absent: $name" }
            $stream = $entry.Open()
            $hash = [Security.Cryptography.SHA256]::Create()
            try { $expected = [Convert]::ToHexString($hash.ComputeHash($stream)).ToLowerInvariant() }
            finally { $hash.Dispose(); $stream.Dispose() }
            $file = Assert-LocalPath $InstallDir (Join-Path $InstallDir $name)
            Assert-ArchiveHash $file $expected
        }
    } finally { $zip.Dispose() }
}

# Fetch a manifest archive into .tools/downloads and verify its SHA256.
function Get-VerifiedArchive([string]$ToolsDir, $Spec, [switch]$Offline) {
    $downloadDir = Assert-LocalPath $ToolsDir (Join-Path $ToolsDir 'downloads')
    [IO.Directory]::CreateDirectory($downloadDir) | Out-Null
    $archiveName = [IO.Path]::GetFileName(([Uri]$Spec.url).AbsolutePath)
    $archive = Assert-LocalPath $ToolsDir (Join-Path $downloadDir $archiveName)
    if (-not (Test-Path -LiteralPath $archive)) {
        if ($Offline) { throw "Offline cache missing: $archiveName" }
        $uri = $Spec.url
        if ($env:JELEE_TOOLS_MIRROR) {
            if ($env:JELEE_TOOLS_MIRROR -notmatch '^https://') { throw 'JELEE_TOOLS_MIRROR must use HTTPS' }
            $uri = $env:JELEE_TOOLS_MIRROR.TrimEnd('/') + '/' + $archiveName
        }
        $partial = Assert-LocalPath $ToolsDir ($archive + '.partial')
        try {
            Write-Host "Downloading $archiveName"
            & curl.exe --fail --location --proto '=https' --proto-redir '=https' --retry 2 --output $partial $uri
            if ($LASTEXITCODE -ne 0) { throw "Download failed: curl exit $LASTEXITCODE" }
            Assert-ArchiveHash $partial $Spec.sha256
            Move-Item -LiteralPath $partial -Destination $archive
        } finally {
            if (Test-Path -LiteralPath $partial) { Remove-Item -LiteralPath $partial -Force }
        }
    }
    try { Assert-ArchiveHash $archive $Spec.sha256 } catch {
        Remove-Item -LiteralPath $archive -Force
        throw
    }
    return $archive
}

# Run a tool with the pinned Go environment (same boundary as run-go.ps1)
# plus the project-local golangci-lint cache; restores the process
# environment afterwards and returns the tool's exit code.
function Invoke-WithGoEnvironment([string]$Root, [string]$Executable, [string[]]$Arguments) {
    $selected = Get-GoSpec $Root
    $goRoot = Assert-LocalPath $Root (Join-Path $Root ".tools/$($selected.Spec.installPath)/go")
    if (-not (Test-Path -LiteralPath (Join-Path $goRoot 'bin/go.exe'))) { throw 'Run scripts/bootstrap-tools.ps1 first' }
    $cache = Assert-LocalPath $Root (Join-Path $Root '.tools/cache')
    $settings = @{
        GOTOOLCHAIN = 'local'; GOENV = 'off'; GOROOT = $goRoot
        GOCACHE = "$cache/go-build"; GOPATH = "$cache/gopath"; GOMODCACHE = "$cache/gomod"
        GOTMPDIR = "$cache/tmp"; TMP = "$cache/tmp"; TEMP = "$cache/tmp"; TMPDIR = "$cache/tmp"
        APPDATA = "$cache/config"; LOCALAPPDATA = "$cache/local"
        XDG_CONFIG_HOME = "$cache/config"; GOLANGCI_LINT_CACHE = "$cache/golangci-lint"
        PATH = (Join-Path $goRoot 'bin') + [IO.Path]::PathSeparator + $env:PATH
    }
    $saved = @{}
    foreach ($name in $settings.Keys) { $saved[$name] = [Environment]::GetEnvironmentVariable($name, 'Process') }
    try {
        foreach ($name in $settings.Keys) { [Environment]::SetEnvironmentVariable($name, $settings[$name], 'Process') }
        foreach ($name in @('GOCACHE','GOPATH','GOMODCACHE','GOTMPDIR','APPDATA','LOCALAPPDATA','GOLANGCI_LINT_CACHE')) {
            [IO.Directory]::CreateDirectory((Assert-LocalPath $Root $settings[$name])) | Out-Null
        }
        & $Executable @Arguments | Out-Host
        $code = $LASTEXITCODE
    } finally {
        foreach ($name in $saved.Keys) { [Environment]::SetEnvironmentVariable($name, $saved[$name], 'Process') }
    }
    return $code
}
