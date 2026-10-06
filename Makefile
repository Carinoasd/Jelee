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
# Same-runner regression gate: base commit vs working tree (CI passes the PR
# base or the previous push); see docs/quality-gates.md.
BENCH_BASE_REF ?=
BENCH_COMPARE_FLAGS ?=

# golangci-lint (G30.1). Findings outside tools/lint-baseline/<goos>.json fail;
# the baseline may only shrink (lint-baseline-prune). CGO is off so every
# host type-checks the same files.
GOLANGCI_LINT := $(CURDIR)/.bin/golangci-lint
LINT_GOOS ?= linux
LINT_REPORT = .testdata/golangci-lint-$(LINT_GOOS).json
LINT_BASELINE = tools/lint-baseline/$(LINT_GOOS).json
# Per-package coverage ratchet (core >=70%, critical >=85%).
COVER_CONFIG := tools/coverage-thresholds.json
COVER_PROFILE ?= .testdata/coverage-gate.out
COVER_PKGS = $(shell $(PYTHON) -c 'import json; print(" ".join("./" + p["path"] for p in json.load(open("$(COVER_CONFIG)"))["packages"]))')

NPM := $(CURDIR)/.bin/npm
WEB := --workspace @jelee/web

.PHONY: backup-drill backup-scale image-memory-test image-memory-smoke-test scan-memory-test scan-memory-smoke-test runtime-memory-test runtime-memory-worker-test memory-contract-test i18n-check family-ignore-sustained-worker-test ignore-sustained-test init bootstrap bootstrap-media bootstrap-matroska matroska-tools-verify matroska-toolchain-test bootstrap-ocr ocr-tools-verify ocr-toolchain-test bootstrap-runtime runtime-tools-verify runtime-toolchain-test probe-runtime-test probe-worker-test nfo-worker-test family-ignore-worker-test ignore-oracle-test sandbox-test tools-verify media-tools-verify tools-clean fixtures fixtures-test build test test-race test-integration coverage fmt fmt-check lint toolchain-test media-toolchain-test brand-scan brand-scan-incremental gitignore-check openapi openapi-check migrate doctor bench bench-check benchgate-test doc-check dev nfo diag web-install web-build web-budget web-test web-lint web-types bootstrap-playwright playwright-verify web-e2e web-visual web-visual-update test-race-nonpostgres test-race-postgres-shard go-test-shard-test golangci-lint lint-baseline-prune coverage-check coverage-ratchet bench-compare quality-gates-test migration-lock migration-lock-check hooks text-check secret-scan secret-scan-history secret-scan-range commit-lint release-check release-dry-run
init: bootstrap
bootstrap:
	sh scripts/bootstrap-tools
bootstrap-media:
	sh scripts/bootstrap-media-tools
media-tools-verify:
	$(PYTHON) scripts/media-tools.py verify
# E4 optional runtime tools (mkvtoolnix, MediaInfo); never part of bootstrap.
bootstrap-matroska:
	$(PYTHON) scripts/matroska-tools.py bootstrap
matroska-tools-verify:
	$(PYTHON) scripts/matroska-tools.py verify
matroska-toolchain-test:
	$(PYTHON) -B scripts/test_matroska_tools.py
# G15.6 optional Tesseract runtime for subtitle OCR (Linux amd64 only);
# never part of bootstrap.
bootstrap-ocr:
	$(PYTHON) scripts/ocr-tools.py bootstrap
ocr-tools-verify:
	$(PYTHON) scripts/ocr-tools.py verify
ocr-toolchain-test:
	$(PYTHON) -B scripts/test_ocr_tools.py
media-toolchain-test:
	$(PYTHON) -B scripts/test_media_tools.py
bootstrap-runtime:
	sh scripts/runtime-tools bootstrap
runtime-tools-verify:
	sh scripts/runtime-tools verify
runtime-toolchain-test:
	$(PYTHON) -B scripts/test_runtime_tools.py
# Targets that build the production image (Dockerfile) need the pinned
# matroska tools it copies: run make bootstrap-matroska first.
probe-runtime-test: matroska-tools-verify
	$(PYTHON) -B scripts/test_probe_runtime.py
probe-worker-test: matroska-tools-verify
	$(PYTHON) -B scripts/test_probe_worker.py
nfo-worker-test: matroska-tools-verify
	$(PYTHON) -B scripts/test_nfo_worker.py
family-ignore-worker-test: matroska-tools-verify
	JELEE_FAMILY_IGNORE_ACCEPTANCE=true $(PYTHON) -B scripts/test_nfo_worker.py
family-ignore-sustained-worker-test: matroska-tools-verify
	JELEE_FAMILY_IGNORE_ACCEPTANCE=true JELEE_FAMILY_IGNORE_SUSTAINED_ACCEPTANCE=true $(PYTHON) -B scripts/test_nfo_worker.py
memory-contract-test:
	$(PYTHON) -B scripts/runtime_memory_contracts.py
runtime-memory-test: memory-contract-test
	$(MAKE) runtime-memory-worker-test
runtime-memory-worker-test: matroska-tools-verify
	JELEE_MEMORY_PROFILE_ACCEPTANCE=true JELEE_FAMILY_IGNORE_ACCEPTANCE=true JELEE_FAMILY_IGNORE_SUSTAINED_ACCEPTANCE=true $(PYTHON) -B scripts/test_nfo_worker.py
scan-memory-test: matroska-tools-verify
	$(PYTHON) -B scripts/test_scan_memory.py
scan-memory-smoke-test: matroska-tools-verify
	$(PYTHON) -B scripts/test_scan_memory.py --smoke
image-memory-test: matroska-tools-verify
	$(PYTHON) -B scripts/test_image_memory.py
image-memory-smoke-test: matroska-tools-verify
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
# Run on the same runner: base commit and head alternate, medians gated with
# runner-noise thresholds (-ns 25 -allocs 10 -bytes 20).
bench-compare:
	@test -n "$(BENCH_BASE_REF)" || { echo 'BENCH_BASE_REF must name the base commit' >&2; exit 2; }
	$(PYTHON) -B scripts/bench-compare.py --go "$(GO)" --base-ref "$(BENCH_BASE_REF)" --packages "$(BENCH_PKGS)" --skip '$(BENCH_SKIP)' $(BENCH_COMPARE_FLAGS)
quality-gates-test:
	"$(GO)" test -count=1 ./tools/lintgate ./tools/covergate ./tools/benchgate ./tools/gitignore-check ./tools/textcheck ./tools/secretscan ./tools/commitlint
	$(PYTHON) -B scripts/test_bench_compare.py
	$(PYTHON) -B scripts/test_release.py
coverage:
	"$(GO)" test -count=1 -coverprofile=coverage.out ./...
coverage-check:
	mkdir -p .testdata
	"$(GO)" test -count=1 -coverprofile="$(COVER_PROFILE)" $(COVER_PKGS)
	"$(GO)" run ./tools/covergate -profile "$(COVER_PROFILE)" -config $(COVER_CONFIG)
# Raise minimums to the measured coverage after adding tests (never lowers).
coverage-ratchet:
	mkdir -p .testdata
	"$(GO)" test -count=1 -coverprofile="$(COVER_PROFILE)" $(COVER_PKGS)
	"$(GO)" run ./tools/covergate -profile "$(COVER_PROFILE)" -config $(COVER_CONFIG) -update
fmt:
	"$(GO)" fmt ./...
fmt-check:
	$(PYTHON) scripts/check-format.py
lint: fmt-check openapi-check doc-check migration-lock-check text-check secret-scan
	"$(GO)" vet ./...
	$(MAKE) --no-print-directory golangci-lint
golangci-lint:
	mkdir -p .testdata
	GOOS=$(LINT_GOOS) CGO_ENABLED=0 "$(GOLANGCI_LINT)" run --issues-exit-code=0 --show-stats=false --output.json.path="$(LINT_REPORT)" ./...
	"$(GO)" run ./tools/lintgate -report "$(LINT_REPORT)" -baseline "$(LINT_BASELINE)"
# After fixing baselined findings: drop them from both baselines. Never adds.
lint-baseline-prune:
	mkdir -p .testdata
	for goos in linux windows; do \
		GOOS=$$goos CGO_ENABLED=0 "$(GOLANGCI_LINT)" run --issues-exit-code=0 --show-stats=false --output.json.path=".testdata/golangci-lint-$$goos.json" ./... && \
		"$(GO)" run ./tools/lintgate -report ".testdata/golangci-lint-$$goos.json" -baseline "tools/lint-baseline/$$goos.json" -prune || exit $$?; \
	done
toolchain-test:
	$(PYTHON) -B scripts/test_toolchain.py
brand-scan:
	"$(GO)" run ./tools/brand-scan
brand-scan-incremental:
	"$(GO)" run ./tools/brand-scan --new
# G01.4b/G01.4c: ignore rules, probes, and every tracked file against
# docs/binary-allowlist.md. GITIGNORE_CHECK_FLAGS=-list also enumerates
# ignored and untracked-not-ignored files.
GITIGNORE_CHECK_FLAGS ?=
gitignore-check:
	"$(GO)" run ./tools/gitignore-check $(GITIGNORE_CHECK_FLAGS)
# G01.5: tracked text files are UTF-8 without BOM with LF endings; binary
# files carry the .gitattributes binary attributes.
text-check:
	"$(GO)" run ./tools/textcheck
# G01.7: secret scan of every tracked file (allowlist:
# tools/secretscan/allowlist.txt); -range for new commits, -history for all.
secret-scan:
	"$(GO)" run ./tools/secretscan
secret-scan-history:
	"$(GO)" run ./tools/secretscan -history
# G01.2/G01.7 for the commits of a push or pull request: COMMIT_RANGE=A..B
# (CI passes the base and head). History before A is not checked.
COMMIT_RANGE ?=
COMMIT_LINT_FLAGS ?=
commit-lint:
	@test -n "$(COMMIT_RANGE)" || { echo 'COMMIT_RANGE must be a revision range, for example origin/master..HEAD' >&2; exit 2; }
	"$(GO)" run ./tools/commitlint -range "$(COMMIT_RANGE)" $(COMMIT_LINT_FLAGS)
secret-scan-range:
	@test -n "$(COMMIT_RANGE)" || { echo 'COMMIT_RANGE must be a revision range, for example origin/master..HEAD' >&2; exit 2; }
	"$(GO)" run ./tools/secretscan -range "$(COMMIT_RANGE)"
# G01.6: use the repository hooks in .githooks for this clone (opt-in; only
# when a developer runs it). `git config --unset core.hooksPath` undoes it.
hooks:
	git config core.hooksPath .githooks
	@echo 'core.hooksPath=.githooks: pre-commit (gofmt, text, secrets, gitignore, brand, web lint) and commit-msg (Conventional Commits)'
# G01.3: RELEASE_TAG=vX.Y.Z validates tag, CHANGELOG section and versions;
# release-dry-run builds the release archives without a tag into
# .testdata/release-dry-run. Neither creates tags or publishes anything.
RELEASE_TAG ?=
release-check:
	@test -n "$(RELEASE_TAG)" || { echo 'RELEASE_TAG must be vMAJOR.MINOR.PATCH' >&2; exit 2; }
	$(PYTHON) -B scripts/release.py check --tag "$(RELEASE_TAG)"
release-dry-run:
	$(PYTHON) -B scripts/release.py dry-run --go "$(GO)"
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
# G04.2 released migrations are immutable (ADR 0006): every migration file is
# locked by SHA-256 in internal/adapter/postgres/migrations/checksums.txt.
# migration-lock appends newly added migrations (never rewrites an entry);
# migration-lock-check fails on a changed, removed, renumbered or unlocked
# migration, and with MIGRATION_LOCK_BASE=<rev> (CI: the PR base) also when
# an entry of that revision's lock was dropped or edited.
MIGRATION_LOCK_BASE ?=
migration-lock:
	"$(GO)" run ./tools/migrationlock -update
migration-lock-check:
	"$(GO)" run ./tools/migrationlock $(if $(MIGRATION_LOCK_BASE),-base "$(MIGRATION_LOCK_BASE)")
# G36.4 backup drill against the dedicated jelee_test database: export,
# restore into a fresh schema, compare every record, import again, plus the
# refusal, mapping and CLI cases. BACKUP_DRILL_REPORT receives a run record
# without connection details (docs/backup-restore.md).
BACKUP_DRILL_REPORT ?= .testdata/backup-drill.txt
backup-drill:
	@test -n "$$JELEE_TEST_DATABASE_URL" || { echo 'JELEE_TEST_DATABASE_URL must name an isolated jelee_test database' >&2; exit 1; }
	mkdir -p "$(dir $(BACKUP_DRILL_REPORT))"
	JELEE_REQUIRE_INTEGRATION=true JELEE_BACKUP_DRILL_REPORT="$(abspath $(BACKUP_DRILL_REPORT))" "$(GO)" test -p 1 -parallel 2 -count=1 -v -timeout 30m \
		-run '^TestMetadataBackup(DrillPostgres|PasswordHashesOptInPostgres|MapsExistingCatalogPostgres|ConflictPreflightPostgres|RejectsDamagedFilesPostgres|ClassifiesEveryTable|ClassifiesEveryTablePostgres|KindsAreWired|CollectionsPlaylistsSettingsPostgres|KeepsLiveUserDataPostgres|PlaylistLimitPostgres)$$' ./internal/adapter/postgres
	JELEE_REQUIRE_INTEGRATION=true "$(GO)" test -p 1 -parallel 2 -count=1 -run '^TestMetadataCLI' ./cmd/jelee-cli
	"$(GO)" test -count=1 -run '^TestMetadataBackup' ./internal/domain
	@echo "backup drill record: $(BACKUP_DRILL_REPORT)"
# 100,000-item export and import with the heap bound (G36.4 scale check).
backup-scale:
	@test -n "$$JELEE_TEST_DATABASE_URL" || { echo 'JELEE_TEST_DATABASE_URL must name an isolated jelee_test database' >&2; exit 1; }
	JELEE_REQUIRE_INTEGRATION=true JELEE_BACKUP_SCALE_ITEMS=100000 "$(GO)" test -p 1 -parallel 2 -count=1 -v -timeout 60m \
		-run '^TestMetadataBackupLargeCatalogMemoryPostgres$$' ./internal/adapter/postgres
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
# web-build also runs the no-playback dist scan and the bundle gzip budget
# (web/bundle-budget.json, G35.4); web-budget re-checks an existing dist.
web-build:
	"$(NPM)" run $(WEB) build
web-budget:
	"$(NPM)" run $(WEB) budget
# End-to-end and visual regression tests (G27.4, G34.5, G34.6), Linux only.
# The Chrome Headless Shell pinned in tools/manifest.json is opt-in (about
# 120 MB) and lives in .tools/playwright; nothing goes to ~/.cache.
# web-visual only compares with web/e2e/__screenshots__; baselines change
# only through web-visual-update, after a person reviewed the new images.
bootstrap-playwright:
	$(PYTHON) scripts/toolchain.py bootstrap --tool playwright
playwright-verify:
	$(PYTHON) scripts/toolchain.py verify --tool playwright
web-e2e:
	"$(NPM)" run $(WEB) e2e
web-visual:
	"$(NPM)" run $(WEB) visual
web-visual-update:
	"$(NPM)" run $(WEB) visual:update
