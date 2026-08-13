# Formae Plugin Makefile
#
# Targets:
#   build   - Build the plugin binary
#   test    - Run tests
#   lint    - Run linter
#   clean   - Remove build artifacts
#   install - Build and install plugin locally (binary + schema + manifest)

# Plugin metadata - extracted from formae-plugin.pkl
PLUGIN_NAME := $(shell pkl eval -x 'name' formae-plugin.pkl 2>/dev/null || echo "example")
PLUGIN_VERSION := $(shell pkl eval -x 'version' formae-plugin.pkl 2>/dev/null || echo "0.0.0")
PLUGIN_NAMESPACE := $(shell pkl eval -x 'namespace' formae-plugin.pkl 2>/dev/null || echo "EXAMPLE")

# Build settings
GO := go
GOFLAGS := -trimpath
BINARY := $(PLUGIN_NAME)

# Installation paths
# Plugin discovery expects lowercase directory names matching the plugin name
PLUGIN_BASE_DIR := $(HOME)/.pel/formae/plugins
INSTALL_DIR := $(PLUGIN_BASE_DIR)/$(PLUGIN_NAME)/v$(PLUGIN_VERSION)

.PHONY: all build test test-unit test-integration lint verify-schema clean install help clean-environment conformance-test conformance-test-crud conformance-test-discovery

all: build

## build: Build the plugin binary and update manifest
build:
	@mkdir -p schema/pkl && echo "$(PLUGIN_VERSION)" > schema/pkl/VERSION
	$(GO) build $(GOFLAGS) -o bin/$(BINARY) .
	@SDK_MIN=$$($(GO) list -m -f '{{.Dir}}' github.com/platform-engineering-labs/formae/pkg/plugin 2>/dev/null | xargs -I{} grep 'MinFormaeVersion' {}/version.go 2>/dev/null | grep -oE '"[0-9]+\.[0-9]+\.[0-9]+"' | tr -d '"'); \
	DECLARED=$$(pkl eval -x minFormaeVersion formae-plugin.pkl 2>/dev/null); \
	EFFECTIVE=$$(printf '%s\n%s\n' "$$SDK_MIN" "$$DECLARED" | grep -E '^[0-9]+\.[0-9]+\.[0-9]+$$' | sort -t. -k1,1n -k2,2n -k3,3n | tail -1); \
	if [ -n "$$EFFECTIVE" ] && [ "$$EFFECTIVE" != "$$DECLARED" ]; then \
		echo "Raising minFormaeVersion to $$EFFECTIVE (sdk=$$SDK_MIN, declared=$$DECLARED)"; \
		if [ "$$(uname)" = "Darwin" ]; then \
			sed -i '' 's/^minFormaeVersion = .*/minFormaeVersion = "'"$$EFFECTIVE"'"/' formae-plugin.pkl; \
		else \
			sed -i 's/^minFormaeVersion = .*/minFormaeVersion = "'"$$EFFECTIVE"'"/' formae-plugin.pkl; \
		fi; \
	else \
		echo "Keeping declared minFormaeVersion=$$DECLARED (sdk=$$SDK_MIN, never downgrade below declared)"; \
	fi

## test: Run all tests
test:
	$(GO) test -v ./...

## test-unit: Run unit tests only (tests with //go:build unit tag)
test-unit:
	$(GO) test -v -tags=unit ./...

## test-integration: Run integration tests (requires cloud credentials)
## Add tests with //go:build integration tag
test-integration:
	$(GO) test -v -tags=integration ./...

## lint: Run golangci-lint
lint:
	golangci-lint run

## verify-schema: Validate PKL schema files
## Checks that schema files are well-formed and follow formae conventions.
verify-schema:
	$(GO) run github.com/platform-engineering-labs/formae/pkg/plugin/testutil/cmd/verify-schema --namespace $(PLUGIN_NAMESPACE) ./schema/pkl

## clean: Remove build artifacts
clean:
	rm -rf bin/ dist/

## install: Build and install plugin locally (binary + schema + manifest)
## Installs to ~/.pel/formae/plugins/<name>/v<version>/
## Removes any existing versions of the plugin first to ensure clean state.
install: build
	@echo "Installing $(PLUGIN_NAME) v$(PLUGIN_VERSION) (namespace: $(PLUGIN_NAMESPACE))..."
	@rm -rf $(PLUGIN_BASE_DIR)/$(PLUGIN_NAME)
	@mkdir -p $(INSTALL_DIR)/schema
	@cp bin/$(BINARY) $(INSTALL_DIR)/$(BINARY)
	@cp -r schema/* $(INSTALL_DIR)/schema/
	@cp formae-plugin.pkl $(INSTALL_DIR)/
	@echo "Installed to $(INSTALL_DIR)"
	@echo "  - Binary: $(INSTALL_DIR)/$(BINARY)"
	@echo "  - Schema: $(INSTALL_DIR)/schema/"
	@echo "  - Manifest: $(INSTALL_DIR)/formae-plugin.pkl"

## help: Show this help message
help:
	@echo "Available targets:"
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/## /  /'

## clean-environment: Clean up test resources in cloud environment
## Called before and after conformance tests. Edit scripts/ci/clean-environment.sh
## to configure for your provider.
clean-environment:
	@./scripts/ci/clean-environment.sh

## conformance-test: Run all conformance tests (CRUD + discovery)
## Usage: make conformance-test [TEST=project] [TIMEOUT=15] [PARALLEL=4] [GOTEST_TIMEOUT=60m]
## Parameters:
##   TEST           - Filter test cases by name pattern (FORMAE_TEST_FILTER)
##   TIMEOUT        - Per-operation timeout in MINUTES (FORMAE_TEST_TIMEOUT, harness default 5)
##   PARALLEL       - Max parallel test cases (FORMAE_TEST_PARALLEL + go test -parallel)
##   TESTDATA_DIR   - Alternate testdata directory (FORMAE_TEST_TESTDATA_DIR)
##   GOTEST_TIMEOUT - Wall-clock limit for the whole go test run (default 60m)
## TIMEOUT is a bare number of minutes, not a Go duration: TIMEOUT=15, not 15m.
## It bounds each individual resource operation; GOTEST_TIMEOUT bounds the run.
## Calls clean-environment before and after tests.
conformance-test: conformance-test-crud conformance-test-discovery

# Env shared by both conformance targets. Empty values are left unset so the
# harness applies its own defaults rather than parsing "".
CONFORMANCE_ENV = FORMAE_TEST_FILTER="$(TEST)" \
	$(if $(TIMEOUT),FORMAE_TEST_TIMEOUT=$(TIMEOUT),) \
	$(if $(PARALLEL),FORMAE_TEST_PARALLEL=$(PARALLEL),) \
	$(if $(TESTDATA_DIR),FORMAE_TEST_TESTDATA_DIR=$(TESTDATA_DIR),)

CONFORMANCE_FLAGS = -tags=conformance -v \
	-timeout $(or $(GOTEST_TIMEOUT),60m) \
	$(if $(PARALLEL),-parallel $(PARALLEL),)

## conformance-test-crud: Run only CRUD lifecycle tests
## Usage: make conformance-test-crud [TEST=project] [TIMEOUT=15] [PARALLEL=4]
conformance-test-crud: install
	@echo "Pre-test cleanup..."
	@./scripts/ci/clean-environment.sh || true
	@echo ""
	@echo "Running CRUD conformance tests..."
	@$(CONFORMANCE_ENV) FORMAE_TEST_TYPE=crud \
		$(GO) test $(CONFORMANCE_FLAGS) ./...; \
	TEST_EXIT=$$?; \
	echo ""; \
	echo "Post-test cleanup..."; \
	./scripts/ci/clean-environment.sh || true; \
	exit $$TEST_EXIT

## conformance-test-discovery: Run only discovery tests
## Usage: make conformance-test-discovery [TEST=project] [TIMEOUT=15] [PARALLEL=4]
conformance-test-discovery: install
	@echo "Pre-test cleanup..."
	@./scripts/ci/clean-environment.sh || true
	@echo ""
	@echo "Running discovery conformance tests..."
	@$(CONFORMANCE_ENV) FORMAE_TEST_TYPE=discovery \
		$(GO) test $(CONFORMANCE_FLAGS) ./...; \
	TEST_EXIT=$$?; \
	echo ""; \
	echo "Post-test cleanup..."; \
	./scripts/ci/clean-environment.sh || true; \
	exit $$TEST_EXIT
