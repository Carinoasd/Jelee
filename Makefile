GO_VERSION := 1.27.1
export PATH := $(CURDIR)/.bin:$(PATH)
GO := $(CURDIR)/.bin/go
PYTHON := python3

.PHONY: scan-memory-test scan-memory-smoke-test runtime-memory-test runtime-memory-worker-test memory-contract-test i18n-check family-ignore-sustained-worker-test ignore-sustained-test init bootstrap bootstrap-media bootstrap-runtime runtime-tools-verify runtime-toolchain-test probe-runtime-test probe-worker-test nfo-worker-test family-ignore-worker-test ignore-oracle-test sandbox-test tools-verify media-tools-verify tools-clean fixtures fixtures-test build test test-race test-integration coverage fmt fmt-check lint toolchain-test media-toolchain-test brand-scan brand-scan-incremental gitignore-check migrate doctor
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
test-integration:
	@test -n "$$JELEE_TEST_DATABASE_URL" || { echo 'JELEE_TEST_DATABASE_URL must name an isolated test database' >&2; exit 1; }
	"$(GO)" test ./internal/adapter/postgres -run Integration -v -count=1
coverage:
	"$(GO)" test -count=1 -coverprofile=coverage.out ./...
fmt:
	"$(GO)" fmt ./...
fmt-check:
	$(PYTHON) scripts/check-format.py
lint: fmt-check
	"$(GO)" vet ./...
toolchain-test:
	$(PYTHON) -B scripts/test_toolchain.py
brand-scan:
	"$(GO)" run ./tools/brand-scan
brand-scan-incremental:
	"$(GO)" run ./tools/brand-scan --new
gitignore-check:
	"$(GO)" run ./tools/gitignore-check
migrate:
	"$(GO)" run ./cmd/jelee-migrate up
doctor:
	"$(GO)" run ./cmd/jelee-cli doctor

i18n-check:
	$(PYTHON) scripts/check-ui-locales.py
