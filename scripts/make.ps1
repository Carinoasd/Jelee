#requires -Version 7.2
[CmdletBinding()]
param([ValidateSet('init','bootstrap','bootstrap-media','bootstrap-runtime','runtime-tools-verify','tools-verify','media-tools-verify','media-toolchain-test','ignore-oracle-test','tools-clean','fixtures','fixtures-test','build','test','test-race','test-integration','coverage','fmt','fmt-check','lint','toolchain-test','brand-scan','brand-scan-incremental','gitignore-check','openapi','openapi-check','migrate','doctor','bench','bench-check','benchgate-test','test','doc-check','dev','nfo','diag')
. "$PSScriptRoot/toolchain-lib.ps1"
$root = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..'))
Push-Location $root
try {
    switch ($Target) {
        { $_ -in 'init','bootstrap' } { & "$PSScriptRoot/bootstrap-tools.ps1" }
        'bootstrap-media' { & "$PSScriptRoot/bootstrap-media-tools.ps1" }
        'media-tools-verify' { & "$PSScriptRoot/media-tools-verify.ps1" }
        'media-toolchain-test' { & "$PSScriptRoot/test-media-tools.ps1" }
        'bootstrap-runtime' { & "$PSScriptRoot/runtime-tools.ps1" -Command bootstrap }
        'runtime-tools-verify' { & "$PSScriptRoot/runtime-tools.ps1" -Command verify }
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
        'test-race' { & "$PSScriptRoot/run-go.ps1" test -race -count=1 -timeout=45m ./... }
        'ignore-oracle-test' {
            $previousRequiredIgnore = $env:JELEE_REQUIRE_IGNORE_ORACLE
            try {
                $env:JELEE_REQUIRE_IGNORE_ORACLE = 'true'
                & "$PSScriptRoot/run-go.ps1" test -count=1 -v -run '^TestGitOracle' ./internal/platform/ignore
            } finally { $env:JELEE_REQUIRE_IGNORE_ORACLE = $previousRequiredIgnore }
        }
        'test-integration' {
            if (-not $env:JELEE_TEST_DATABASE_URL) { throw 'JELEE_TEST_DATABASE_URL must name an isolated test database' }
            & "$PSScriptRoot/run-go.ps1" test ./internal/adapter/postgres -run Integration -v -count=1
        }
        { $_ -in 'bench','bench-check' } {
            # Mirrors the Makefile bench variables; override through JELEE_BENCH_* environment variables.
            $benchPackages = @('./internal/access','./internal/domain','./internal/domain/medianame','./internal/adapter/nfo','./internal/adapter/images','./internal/adapter/subtitles','./internal/adapter/http')
            $pattern = if ($env:JELEE_BENCH) { $env:JELEE_BENCH } else { '.' }
            $skip = if ($env:JELEE_BENCH_SKIP) { $env:JELEE_BENCH_SKIP } else { 'ObservedHundredFiles' }
            $count = if ($env:JELEE_BENCH_COUNT) { $env:JELEE_BENCH_COUNT } else { '6' }
            $benchTime = if ($env:JELEE_BENCH_TIME) { $env:JELEE_BENCH_TIME } else { '500ms' }
            $current = Assert-LocalPath $root (Join-Path $root '.testdata/bench-current.txt')
            [IO.Directory]::CreateDirectory((Split-Path -Parent $current)) | Out-Null
            & "$PSScriptRoot/run-go.ps1" test -run '^$' -bench $pattern -skip $skip -benchmem "-count=$count" "-benchtime=$benchTime" @benchPackages > $current
            Write-Host "benchmark output written to $current"
            if ($Target -eq 'bench-check') {
                & "$PSScriptRoot/run-go.ps1" run ./tools/benchgate -base docs/evidence/bench-baseline.txt -current $current
            }
        }
        'benchgate-test' { & "$PSScriptRoot/run-go.ps1" test -count=1 ./tools/benchgate }
        'coverage' { & "$PSScriptRoot/run-go.ps1" test -count=1 -coverprofile=coverage.out ./... }
        'fmt' { & "$PSScriptRoot/run-go.ps1" fmt ./... }
        'fmt-check' {
            $selected = Get-GoSpec $root
            $gofmt = Join-Path $root ".tools/$($selected.Spec.installPath)/go/bin/gofmt.exe"
            # gofmt collects Go telemetry too; reuse the project config boundary.
            $previousAppData = $env:APPDATA
            try {
                $env:APPDATA = Assert-LocalPath $root (Join-Path $root '.tools/cache/config')
                [IO.Directory]::CreateDirectory($env:APPDATA) | Out-Null
                # Let gofmt walk directories; absolute per-file arguments exceed Windows' command-line limit in long checkouts.
                $unformatted = @(& $gofmt -l cmd internal tools)
                if ($LASTEXITCODE -ne 0) { throw 'gofmt failed' }
            } finally { $env:APPDATA = $previousAppData }
            if ($unformatted.Count) { throw "Run scripts/make.ps1 fmt. Unformatted files: $($unformatted -join ', ')" }
        }
        'lint' {
            & "$PSScriptRoot/make.ps1" fmt-check
            & "$PSScriptRoot/make.ps1" openapi-check
            & "$PSScriptRoot/make.ps1" doc-check
            & "$PSScriptRoot/run-go.ps1" vet ./...
        }
        'toolchain-test' { & "$PSScriptRoot/test-toolchain.ps1" }
        'brand-scan' { & "$PSScriptRoot/run-go.ps1" run ./tools/brand-scan }
        'brand-scan-incremental' { & "$PSScriptRoot/run-go.ps1" run ./tools/brand-scan --new }
        'gitignore-check' { & "$PSScriptRoot/run-go.ps1" run ./tools/gitignore-check }
        'openapi' { & "$PSScriptRoot/run-go.ps1" run ./tools/openapi }
        'openapi-check' {
            & "$PSScriptRoot/run-go.ps1" run ./tools/openapi -check
            & "$PSScriptRoot/run-go.ps1" test -count=1 -run OpenAPI ./tools/openapi ./internal/adapter/http
        }
        'migrate' { & "$PSScriptRoot/run-go.ps1" run ./cmd/jelee-migrate up }
        'doctor' { & "$PSScriptRoot/run-go.ps1" run ./cmd/jelee-cli doctor }
        'doc-check' { & "$PSScriptRoot/run-go.ps1" run ./tools/doccheck }
        'dev' {
            if (-not ($env:JELEE_DATABASE_URL -or $env:JELEE_DATABASE_URL_FILE -or $env:JELEE_CONFIG)) {
                throw 'Set JELEE_DATABASE_URL (or JELEE_DATABASE_URL_FILE / JELEE_CONFIG) to a development database, then run scripts/make.ps1 migrate'
            }
            & "$PSScriptRoot/run-go.ps1" run ./cmd/jelee
        }
        'nfo' {
            # Same contract as `make nfo`: NFO_ROOT/NFO_FILE validate one file, otherwise offline NFO tests.
            if ($env:NFO_ROOT -or $env:NFO_FILE) {
                if (-not ($env:NFO_ROOT -and $env:NFO_FILE)) { throw 'Set both NFO_ROOT and NFO_FILE' }
                & "$PSScriptRoot/run-go.ps1" run ./cmd/jelee-cli nfo validate --root $env:NFO_ROOT --file $env:NFO_FILE
            } else {
                & "$PSScriptRoot/run-go.ps1" test -count=1 ./internal/adapter/nfo
                & "$PSScriptRoot/run-go.ps1" test -count=1 -run 'NFO|Nfo' ./internal/domain ./internal/app ./cmd/jelee-cli
            }
        }
        'diag' {
            # `jelee-cli diag export` (G50.2) is not implemented; run every existing doctor check, then fail if any did.
            $failed = @()
            foreach ($check in @(@('doctor','tools'), @('doctor','probe'), @('doctor'))) {
                Write-Host "== jelee-cli $($check -join ' ')"
                try { & "$PSScriptRoot/run-go.ps1" run ./cmd/jelee-cli @check } catch { $failed += ($check -join ' ') }
            }
            if ($failed.Count) { throw "Diagnostics failed: $($failed -join ', ')" }
        }
    }
} finally { Pop-Location }
