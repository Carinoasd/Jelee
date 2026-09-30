#requires -Version 7.2
. "$PSScriptRoot/media-tools-lib.ps1"
Test-MediaInstallation ([IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..')))
