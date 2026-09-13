.PHONY: generate verify-generated web-deps test-deps build images deploy-kind reset-kind \
	test-unit lint catalog-package e2e-prepare e2e-reset test-e2e test-e2e-node \
	test-e2e-k8s test-e2e-recovery test-acceptance-node test-acceptance-mcp test-vk8s-network \
	docs-sync docs-build docs-package docs-check

VERSION ?= 0.1.0
BUILD_TIME := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
COMMIT := $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
LDFLAGS := -s -w \
	-X 'github.com/breakfix/breakfix/internal/buildinfo.Version=$(VERSION)' \
	-X 'github.com/breakfix/breakfix/internal/buildinfo.BuildTime=$(BUILD_TIME)' \
	-X 'github.com/breakfix/breakfix/internal/buildinfo.Commit=$(COMMIT)'

CONTROLLER_GEN_VERSION := v0.21.0
CONTROLLER_GEN := go run sigs.k8s.io/controller-tools/cmd/controller-gen@$(CONTROLLER_GEN_VERSION)
CRD_TYPES_DIR := api/v1
OAPI_CODEGEN_VERSION := v2.7.1
OAPI_CODEGEN := go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@$(OAPI_CODEGEN_VERSION)
OPENAPI_SPEC := api/http/openapi.yaml
OPENAPI_GO_CONFIG := api/http/oapi-codegen.yaml
OPENAPI_GO_OUTPUT := internal/transport/httpapi/generated/server.gen.go
OPENAPI_FRONTEND_OUTPUT := web/src/api/generated
OPENAPI_TS := npm exec --prefix web -- openapi-ts
OPENAPI_TS_ARGS := -i $(CURDIR)/$(OPENAPI_SPEC) -p @hey-api/typescript --no-log-file

WEB_DIR := web
WEB_DIST := $(WEB_DIR)/dist
WEB_EMBED_DIR := internal/transport/httpapi/ui/assets
WEB_DEPS_STAMP := $(WEB_DIR)/node_modules/.breakfix-deps
TEST_DIR := test
TEST_DEPS_STAMP := $(TEST_DIR)/node_modules/.breakfix-deps
BIN_DIR := bin
TARGETOS ?= linux
TARGETARCH ?= amd64
RUNTIME_IMAGE_REPOSITORY ?= ghcr.io/breakfix
RUNTIME_IMAGE_TAG ?= dev
DOCS_SITE_SCRIPT := $(CURDIR)/docs-site/scripts/docs-site.sh

CATALOG_SOURCE ?=
CATALOG_ARCHIVE ?= dist/catalog.oci.tar
CATALOG_REFERENCE ?=
CATALOG_TRUST_BUNDLE_FILE ?=
CATALOG_BUNDLE ?=

$(WEB_DEPS_STAMP): $(WEB_DIR)/package.json $(WEB_DIR)/package-lock.json
	npm ci --prefix $(WEB_DIR)
	@touch $@

web-deps: $(WEB_DEPS_STAMP)

$(TEST_DEPS_STAMP): $(TEST_DIR)/package.json $(TEST_DIR)/package-lock.json
	npm ci --prefix $(TEST_DIR)
	@touch $@

test-deps: $(TEST_DEPS_STAMP)

docs-sync:
	$(DOCS_SITE_SCRIPT) sync

docs-build:
	$(DOCS_SITE_SCRIPT) build

docs-package:
	$(DOCS_SITE_SCRIPT) package

docs-check:
	$(DOCS_SITE_SCRIPT) check

generate: web-deps
	$(CONTROLLER_GEN) object paths=./$(CRD_TYPES_DIR)
	$(CONTROLLER_GEN) crd:crdVersions=v1 paths=./$(CRD_TYPES_DIR) output:crd:dir=deploy/crds
	$(OAPI_CODEGEN) --config $(OPENAPI_GO_CONFIG) $(OPENAPI_SPEC)
	$(OPENAPI_TS) $(OPENAPI_TS_ARGS) -o $(CURDIR)/$(OPENAPI_FRONTEND_OUTPUT)

verify-generated: web-deps
	@tmp=$$(mktemp -d); \
	trap 'rm -rf "$$tmp"' EXIT; \
	$(CONTROLLER_GEN) object paths=./$(CRD_TYPES_DIR) output:dir=$$tmp; \
	$(CONTROLLER_GEN) crd:crdVersions=v1 paths=./$(CRD_TYPES_DIR) output:crd:dir=$$tmp/crd; \
	diff -u $(CRD_TYPES_DIR)/zz_generated.deepcopy.go $$tmp/zz_generated.deepcopy.go; \
	diff -ru deploy/crds $$tmp/crd; \
	sed "s|^output:.*|output: $$tmp/server.gen.go|" $(OPENAPI_GO_CONFIG) >"$$tmp/oapi-codegen.yaml" && \
	$(OAPI_CODEGEN) --config "$$tmp/oapi-codegen.yaml" $(OPENAPI_SPEC) && \
	$(OPENAPI_TS) $(OPENAPI_TS_ARGS) -o "$$tmp/frontend" && \
	diff -u $(OPENAPI_GO_OUTPUT) "$$tmp/server.gen.go" && \
	diff -ru $(OPENAPI_FRONTEND_OUTPUT) "$$tmp/frontend"

build: web-deps
	npm run build --prefix $(WEB_DIR)
	@mkdir -p $(WEB_EMBED_DIR)
	@find $(WEB_EMBED_DIR) -mindepth 1 ! -name .gitkeep -delete
	@cp -a $(WEB_DIST)/. $(WEB_EMBED_DIR)/
	@mkdir -p $(BIN_DIR)
	CGO_ENABLED=0 GOOS=$(TARGETOS) GOARCH=$(TARGETARCH) go build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/breakfix-server ./cmd/server
	CGO_ENABLED=0 GOOS=$(TARGETOS) GOARCH=$(TARGETARCH) go build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/breakfix-controller ./cmd/controller
	CGO_ENABLED=0 GOOS=$(TARGETOS) GOARCH=$(TARGETARCH) go build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/breakfix-runtime-worker ./cmd/runtime-worker
	CGO_ENABLED=0 GOOS=$(TARGETOS) GOARCH=$(TARGETARCH) go build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/breakfix-mcp ./cmd/breakfix-mcp

images: build
	docker build --platform $(TARGETOS)/$(TARGETARCH) --provenance=false -t $(RUNTIME_IMAGE_REPOSITORY)/breakfix-server:$(RUNTIME_IMAGE_TAG) -f build/images/server/Dockerfile $(BIN_DIR)
	docker build --platform $(TARGETOS)/$(TARGETARCH) --provenance=false -t $(RUNTIME_IMAGE_REPOSITORY)/breakfix-controller:$(RUNTIME_IMAGE_TAG) -f build/images/controller/Dockerfile $(BIN_DIR)
	docker build --platform $(TARGETOS)/$(TARGETARCH) --provenance=false -t $(RUNTIME_IMAGE_REPOSITORY)/breakfix-runtime-worker:$(RUNTIME_IMAGE_TAG) -f build/images/runtime-worker/Dockerfile $(BIN_DIR)
	docker build --platform $(TARGETOS)/$(TARGETARCH) --provenance=false -t breakfix-k8s-base:latest build/images/k8s-base

deploy-kind: images
	./scripts/kind/runtime.sh

reset-kind:
	./scripts/kind/reset-state.sh

test-unit:
	go test -count=1 ./...

lint:
	golangci-lint run ./...

catalog-package:
	@test -n "$(CATALOG_SOURCE)" || { echo "CATALOG_SOURCE must name a portable Catalog Release source"; exit 2; }
	@test -f "$(CATALOG_SOURCE)/release.yaml" || { echo "$(CATALOG_SOURCE) does not contain release.yaml"; exit 2; }
	go run ./cmd/catalog-release -source "$(CATALOG_SOURCE)" -output "$(CATALOG_ARCHIVE)" $(if $(CATALOG_REFERENCE),-reference "$(CATALOG_REFERENCE)") $(if $(CATALOG_TRUST_BUNDLE_FILE),-trust-bundle-file "$(CATALOG_TRUST_BUNDLE_FILE)")

test-e2e: test-deps
	./scripts/kind/run-e2e.sh ui

e2e-prepare:
	./scripts/kind/e2e-prepare.sh

e2e-reset:
	./scripts/kind/e2e-target.sh reset

test-e2e-node: test-deps
	./scripts/kind/run-e2e.sh node

test-e2e-k8s: test-deps
	./scripts/kind/run-e2e.sh k8s

test-e2e-recovery: test-deps
	./scripts/kind/run-e2e.sh recovery

test-acceptance-node: test-deps
	@test "$(RUN_AGENT_LIVE_E2E)" = "1" || { echo "RUN_AGENT_LIVE_E2E=1 is required for live Node acceptance" >&2; exit 2; }
	$(MAKE) --no-print-directory e2e-prepare
	RUN_AGENT_LIVE_E2E=1 ./scripts/kind/run-e2e.sh acceptance-node

test-acceptance-mcp: test-deps
	@test "$(RUN_AGENT_LIVE_E2E)" = "1" || { echo "RUN_AGENT_LIVE_E2E=1 is required for live MCP acceptance" >&2; exit 2; }
	$(MAKE) --no-print-directory e2e-prepare
	RUN_AGENT_LIVE_E2E=1 ./scripts/kind/run-e2e.sh acceptance-mcp

test-acceptance-interruption: test-deps
	@test "$(RUN_AGENT_LIVE_E2E)" = "1" || { echo "RUN_AGENT_LIVE_E2E=1 is required for live interruption acceptance" >&2; exit 2; }
	RUN_AGENT_LIVE_E2E=1 ./scripts/kind/run-authoring-interruption-e2e.sh

test-vk8s-network:
	./scripts/kind/verify-vk8s-network-isolation.sh
