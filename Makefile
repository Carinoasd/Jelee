GO_VERSION := 1.27.1
export PATH := $(CURDIR)/.bin:$(PATH)
GO := $(CURDIR)/.bin/go
PYTHON := python3
# Hot-path benchmarks (G26.1/G26.4). BENCH_COUNT repetitions are reduced to a
# median by tools/benchgate; BENCH_SKIP drops disk-bound benchmarks too noisy
# for a percentage gate.
BENCH_PKGS := ./internal/access ./internal/domain ./internal/domain/medianame ./internal/adapter/nfo ./internal/adapter/images ./internal/adapter/subtitles ./internal/adapter/http
BENCH ?= .
BENCH_SKIP ?= ObservedHundredFiles
BENCH_COUNT ?= 6
BENCH_TIME ?= 500ms
BENCH_CURRENT ?= .testdata/bench-current.txt
BENCH_BASELINE ?= docs/evidence/bench-baseline.txt
BENCHGATE_FLAGS ?=

NPM := $(CURDIR)/.bin/npm
WEB := --workspace @jelee/web

.PHONY: image-memory-test image-memory-smoke-test scan-memory-test scan-memory-smoke-test runtime-memory-test runtime-memory-worker-test memory-contract-test i18n-check family-ignore-sustained-worker-test ignore-sustained-test init bootstrap bootstrap-media bootstrap-runtime runtime-tools-verify runtime-toolchain-test probe-runtime-test probe-worker-test nfo-worker-test family-ignore-worker-test ignore-oracle-test sandbox-test tools-verify media-tools-verify tools-clean fixtures fixtures-test build test test-race test-integration coverage fmt fmt-check lint toolchain-test media-toolchain-test brand-scan brand-scan-incremental gitignore-check openapi openapi-check migrate doctor bench bench-check benchgate-test doc-check dev nfo diag web-install web-build web-test web-lint web-types test-race-nonpostgres test-race-postgres-shard go-test-shard-test
init: bootstrap
bootstrap:
	sh scripts/bootstrap-tools
bootstrap-media:
	sh scripts/bootstrap-media-tools
media-tools-verify:
	$(PYTHON) scripts/media-tools.py verify
media-toolchain-test:
	$(PYTHON) -B scripts/test_media_tools.py
bootstrap-runtime:
	sh scripts/runtime-tools bootstrap
runtime-tools-verify:
	sh scripts/runtime-tools verify
runtime-toolchain-test:
	$(PYTHON) -B scripts/test_runtime_tools.py
probe-runtime-test:
	$(PYTHON) -B scripts/test_probe_runtime.py
probe-worker-test:
	$(PYTHON) -B scripts/test_probe_worker.py
nfo-worker-test:
	$(PYTHON) -B scripts/test_nfo_worker.py
family-ignore-worker-test:
	JELEE_FAMILY_IGNORE_ACCEPTANCE=true $(PYTHON) -B scripts/test_nfo_worker.py
family-ignore-sustained-worker-test:
	JELEE_FAMILY_IGNORE_ACCEPTANCE=true JELEE_FAMILY_IGNORE_SUSTAINED_ACCEPTANCE=true $(PYTHON) -B scripts/test_nfo_worker.py
memory-contract-test:
	$(PYTHON) -B scripts/runtime_memory_contracts.py
runtime-memory-test: memory-contract-test
	$(MAKE) runtime-memory-worker-test
runtime-memory-worker-test:
	JELEE_MEMORY_PROFILE_ACCEPTANCE=true JELEE_FAMILY_IGNORE_ACCEPTANCE=true JELEE_FAMILY_IGNORE_SUSTAINED_ACCEPTANCE=true $(PYTHON) -B scripts/test_nfo_worker.py
scan-memory-test:
	$(PYTHON) -B scripts/test_scan_memory.py
scan-memory-smoke-test:
	$(PYTHON) -B scripts/test_scan_memory.py --smoke
image-memory-test:
	$(PYTHON) -B scripts/test_image_memory.py
image-memory-smoke-test:
	$(PYTHON) -B scripts/test_image_memory.py --smoke
ignore-oracle-test:
	JELEE_REQUIRE_IGNORE_ORACLE=true "$(GO)" test -count=1 -v -run '^TestGitOracle' ./internal/platform/ignore
sandbox-test:
	$(PYTHON) -B scripts/test_sandbox_native.py
fixtures: bootstrap-media media-tools-verify
	sh scripts/gen-fixtures
fixtures-test:
	$(PYTHON) -B scripts/test_fixtures.py
	JELEE_REQUIRE_MEDIA_TOOL_TESTS=true "$(GO)" test -tags jelee_fixture_tools -count=1 -v ./tools/gen-fixtures
	"$(GO)" test -tags jelee_fixture_tools -run TestFixtureBuild -count=1 -v ./internal/platform/process
	JELEE_REQUIRE_MEDIA_TOOL_TESTS=true "$(GO)" test -run TestPinnedInstalledFFprobe -count=1 -v ./internal/platform/toolidentity
tools-verify:
	$(PYTHON) scripts/toolchain.py verify
tools-clean:
	$(PYTHON) scripts/toolchain.py clean
build:
	mkdir -p bin
	"$(GO)" build -trimpath -o bin/jelee ./cmd/jelee
	"$(GO)" build -trimpath -o bin/jelee-migrate ./cmd/jelee-migrate
	"$(GO)" build -trimpath -o bin/jelee-cli ./cmd/jelee-cli
test:
	"$(GO)" test -count=1 ./...
ignore-sustained-test:
	$(PYTHON) scripts/test_ignore_sustained.py
test-race:
	"$(GO)" test -race -count=1 -timeout=45m ./...
# CI splits the PostgreSQL repository package across jobs: it no longer fits a
# single 45-minute race run against a real database. SHARD is I/N.
POSTGRES_PKG := ./internal/adapter/postgres
test-race-nonpostgres:
	"$(GO)" test -race -count=1 -timeout=45m $$("$(GO)" list ./... | grep -v '/internal/adapter/postgres$$')
test-race-postgres-shard:
	@test -n "$(SHARD)" || { echo 'SHARD must be I/N, for example SHARD=1/4' >&2; exit 2; }
	$(PYTHON) -B scripts/go_test_shard.py --go "$(GO)" --package $(POSTGRES_PKG) --shard $(SHARD) -- -race -count=1 -timeout=45m
go-test-shard-test:
	$(PYTHON) -B scripts/test_go_test_shard.py
test-integration:
	@test -n "$$JELEE_TEST_DATABASE_URL" || { echo 'JELEE_TEST_DATABASE_URL must name an isolated test database' >&2; exit 1; }
	"$(GO)" test ./internal/adapter/postgres -run Integration -v -count=1
# Run the hot-path benchmarks into $(BENCH_CURRENT).
bench:
	mkdir -p "$(dir $(BENCH_CURRENT))"
	"$(GO)" test -run '^$$' -bench '$(BENCH)' -skip '$(BENCH_SKIP)' -benchmem -count=$(BENCH_COUNT) -benchtime=$(BENCH_TIME) $(BENCH_PKGS) > "$(BENCH_CURRENT)"
	@echo "benchmark output written to $(BENCH_CURRENT)"
# Fail when a median regresses past the thresholds (ns/op +15%, allocs/op +10%).
bench-check: bench
	"$(GO)" run ./tools/benchgate -base "$(BENCH_BASELINE)" -current "$(BENCH_CURRENT)" $(BENCHGATE_FLAGS)
benchgate-test:
	"$(GO)" test -count=1 ./tools/benchgate
coverage:
	"$(GO)" test -count=1 -coverprofile=coverage.out ./...
fmt:
	"$(GO)" fmt ./...
fmt-check:
	$(PYTHON) scripts/check-format.py
lint: fmt-check openapi-check doc-check
	"$(GO)" vet ./...
toolchain-test:
	$(PYTHON) -B scripts/test_toolchain.py
brand-scan:
	"$(GO)" run ./tools/brand-scan
brand-scan-incremental:
	"$(GO)" run ./tools/brand-scan --new
gitignore-check:
	"$(GO)" run ./tools/gitignore-check
# Regenerate api/openapi.json from the router's specification code.
openapi:
	"$(GO)" run ./tools/openapi
# Offline OpenAPI gate: committed document is current, every route is
# documented and every documented path is routed, error codes are described.
openapi-check:
	"$(GO)" run ./tools/openapi -check
	"$(GO)" test -count=1 -run OpenAPI ./tools/openapi ./internal/adapter/http
migrate:
	"$(GO)" run ./cmd/jelee-migrate up
doctor:
	"$(GO)" run ./cmd/jelee-cli doctor
# Offline documentation gate (G49.8): relative links and anchors in README.md
# and docs/, external link format (no network) and documented error codes.
doc-check:
	"$(GO)" run ./tools/doccheck
# Local development server on JELEE_LISTEN (default 127.0.0.1:8097). Needs a
# migrated database: set JELEE_DATABASE_URL (or _FILE / JELEE_CONFIG), then
# run `make migrate` once.
dev:
	@test -n "$$JELEE_DATABASE_URL$$JELEE_DATABASE_URL_FILE$$JELEE_CONFIG" || { echo 'make dev: set JELEE_DATABASE_URL (or JELEE_DATABASE_URL_FILE / JELEE_CONFIG) to a development database, then run make migrate' >&2; exit 2; }
	"$(GO)" run ./cmd/jelee
# NFO checks without a database. With NFO_ROOT (absolute media root) and
# NFO_FILE (root-relative .nfo) it validates that file read-only; otherwise it
# runs the offline NFO reader/writer test suites.
nfo:
	@if [ -n "$(NFO_ROOT)$(NFO_FILE)" ]; then \
		if [ -z "$(NFO_ROOT)" ] || [ -z "$(NFO_FILE)" ]; then echo 'make nfo: set both NFO_ROOT and NFO_FILE' >&2; exit 2; fi; \
		"$(GO)" run ./cmd/jelee-cli nfo validate --root "$(NFO_ROOT)" --file "$(NFO_FILE)"; \
	else \
		"$(GO)" test -count=1 ./internal/adapter/nfo && \
		"$(GO)" test -count=1 -run 'NFO|Nfo' ./internal/domain ./internal/app ./cmd/jelee-cli; \
	fi
DIAG_OUT ?= .testdata/jelee-diag.zip
diag:
	"$(GO)" run ./cmd/jelee-cli diag export --out "$(DIAG_OUT)"

i18n-check:
	$(PYTHON) scripts/check-ui-locales.py

# Web frontend (G31/G27). Uses the manifest-pinned Node from `make bootstrap`;
# dependency lifecycle scripts never run (also enforced by .npmrc).
web-install:
	"$(NPM)" ci --ignore-scripts
web-types:
	"$(NPM)" run $(WEB) types
web-lint:
	"$(NPM)" run $(WEB) lint
web-test:
	"$(NPM)" run $(WEB) test
web-build:
	"$(NPM)" run $(WEB) build
