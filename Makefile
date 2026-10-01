GO_VERSION := 1.27.1
export PATH := $(CURDIR)/.bin:$(PATH)
GO := $(CURDIR)/.bin/go
PYTHON := python3

.PHONY: init bootstrap tools-verify tools-clean build test test-race test-integration coverage fmt fmt-check lint toolchain-test brand-scan brand-scan-incremental gitignore-check migrate doctor
init: bootstrap
bootstrap:
	sh scripts/bootstrap-tools
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
test-race:
	"$(GO)" test -race -count=1 ./...
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
