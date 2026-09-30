#requires -Version 7.2
[CmdletBinding()]
param([ValidateSet('init','bootstrap','bootstrap-media','tools-verify','media-tools-verify','media-toolchain-test','tools-clean','fixtures','fixtures-test','build','test','test-race','test-integration','coverage','fmt','fmt-check','lint','toolchain-test','brand-scan','brand-scan-incremental','gitignore-check','migrate','doctor')][string]$Target = 'test')
. "$PSScriptRoot/toolchain-lib.ps1"
$root = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..'))
Push-Location $root
try {
    switch ($Target) {
        { $_ -in 'init','bootstrap' } { & "$PSScriptRoot/bootstrap-tools.ps1" }
        'bootstrap-media' { & "$PSScriptRoot/bootstrap-media-tools.ps1" }
        'media-tools-verify' { & "$PSScriptRoot/media-tools-verify.ps1" }
        'media-toolchain-test' { & "$PSScriptRoot/test-media-tools.ps1" }
        'fixtures' {
            & "$PSScriptRoot/bootstrap-media-tools.ps1"
            & "$PSScriptRoot/media-tools-verify.ps1"
            & "$PSScriptRoot/gen-fixtures.ps1"
        }
        'fixtures-test' {
            $previousRequiredMedia = $env:JELEE_REQUIRE_MEDIA_TOOL_TESTS
            try {
                $env:JELEE_REQUIRE_MEDIA_TOOL_TESTS = 'true'
                & "$PSScriptRoot/run-go.ps1" test -tags jelee_fixture_tools -count=1 -v ./tools/gen-fixtures
                & "$PSScriptRoot/run-go.ps1" test -tags jelee_fixture_tools -run TestFixtureBuild -count=1 -v ./internal/platform/process
                & "$PSScriptRoot/run-go.ps1" test -run TestPinnedInstalledFFprobe -count=1 -v ./internal/platform/toolidentity
            } finally { $env:JELEE_REQUIRE_MEDIA_TOOL_TESTS = $previousRequiredMedia }
        }
        'tools-verify' { & "$PSScriptRoot/tools-verify.ps1" }
        'tools-clean' {
            foreach ($name in @('.tools','.bin','.testfixtures','.testdata')) { Remove-LocalTree $root (Join-Path $root $name) }
            Write-Host 'Removed local tools, caches and generated test data'
        }
        'build' {
            $bin = Assert-LocalPath $root (Join-Path $root 'bin')
            [IO.Directory]::CreateDirectory($bin) | Out-Null
            foreach ($command in @('jelee','jelee-migrate','jelee-cli')) {
                & "$PSScriptRoot/run-go.ps1" build -trimpath -o "$bin/$command.exe" "./cmd/$command"
            }
        }
        'test' { & "$PSScriptRoot/run-go.ps1" test -count=1 ./... }
        'test-race' { & "$PSScriptRoot/run-go.ps1" test -race -count=1 ./... }
        'test-integration' {
            if (-not $env:JELEE_TEST_DATABASE_URL) { throw 'JELEE_TEST_DATABASE_URL must name an isolated test database' }
            & "$PSScriptRoot/run-go.ps1" test ./internal/adapter/postgres -run Integration -v -count=1
        }
        'coverage' { & "$PSScriptRoot/run-go.ps1" test -count=1 -coverprofile=coverage.out ./... }
        'fmt' { & "$PSScriptRoot/run-go.ps1" fmt ./... }
        'fmt-check' {
            $selected = Get-GoSpec $root
            $gofmt = Join-Path $root ".tools/$($selected.Spec.installPath)/go/bin/gofmt.exe"
            $files = Get-ChildItem -LiteralPath cmd,internal,tools -Filter '*.go' -Recurse -File | Select-Object -ExpandProperty FullName
            # gofmt collects Go telemetry too; reuse the project config boundary.
            $previousAppData = $env:APPDATA
            try {
                $env:APPDATA = Assert-LocalPath $root (Join-Path $root '.tools/cache/config')
                [IO.Directory]::CreateDirectory($env:APPDATA) | Out-Null
                $unformatted = @(& $gofmt -l $files)
                if ($LASTEXITCODE -ne 0) { throw 'gofmt failed' }
            } finally { $env:APPDATA = $previousAppData }
            if ($unformatted.Count) { throw "Run scripts/make.ps1 fmt. Unformatted files: $($unformatted -join ', ')" }
        }
        'lint' {
            & "$PSScriptRoot/make.ps1" fmt-check
            & "$PSScriptRoot/run-go.ps1" vet ./...
        }
        'toolchain-test' { & "$PSScriptRoot/test-toolchain.ps1" }
        'brand-scan' { & "$PSScriptRoot/run-go.ps1" run ./tools/brand-scan }
        'brand-scan-incremental' { & "$PSScriptRoot/run-go.ps1" run ./tools/brand-scan --new }
        'gitignore-check' { & "$PSScriptRoot/run-go.ps1" run ./tools/gitignore-check }
        'migrate' { & "$PSScriptRoot/run-go.ps1" run ./cmd/jelee-migrate up }
        'doctor' { & "$PSScriptRoot/run-go.ps1" run ./cmd/jelee-cli doctor }
    }
} finally { Pop-Location }
