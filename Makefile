.PHONY: generate verify-generated build images deploy-kind reset-kind test-unit \
	catalog-package catalog-install test-e2e

VERSION ?= 0.1.0
BUILD_TIME := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
COMMIT := $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
LDFLAGS := -s -w \
	-X 'github.com/breakfix/breakfix/internal/build.Version=$(VERSION)' \
	-X 'github.com/breakfix/breakfix/internal/build.BuildTime=$(BUILD_TIME)' \
	-X 'github.com/breakfix/breakfix/internal/build.Commit=$(COMMIT)'

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
BIN_DIR := bin
TARGETOS ?= linux
TARGETARCH ?= amd64
RUNTIME_IMAGE_REPOSITORY ?= ghcr.io/breakfix
RUNTIME_IMAGE_TAG ?= dev

CATALOG_SOURCE ?= catalog
CATALOG_ARCHIVE ?= dist/catalog.oci.tar
CATALOG_REFERENCE ?=
CATALOG_REGISTRY_ENDPOINT ?=
CATALOG_TRUST_BUNDLE_FILE ?=
CATALOG_BUNDLE ?=
CATALOG_SERVER_URL ?= http://localhost:9090
CATALOG_ADMIN_TOKEN ?= $(BREAKFIX_CATALOG_ADMIN_TOKEN)

generate:
	$(CONTROLLER_GEN) object paths=./$(CRD_TYPES_DIR)
	$(CONTROLLER_GEN) crd:crdVersions=v1 paths=./$(CRD_TYPES_DIR) output:crd:dir=deploy/crds
	$(OAPI_CODEGEN) --config $(OPENAPI_GO_CONFIG) $(OPENAPI_SPEC)
	$(OPENAPI_TS) $(OPENAPI_TS_ARGS) -o $(CURDIR)/$(OPENAPI_FRONTEND_OUTPUT)

verify-generated:
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

build:
	npm ci --prefix $(WEB_DIR)
	npm run build --prefix $(WEB_DIR)
	@mkdir -p $(WEB_EMBED_DIR)
	@find $(WEB_EMBED_DIR) -mindepth 1 ! -name .gitkeep -delete
	@cp -a $(WEB_DIST)/. $(WEB_EMBED_DIR)/
	@mkdir -p $(BIN_DIR)
	CGO_ENABLED=0 GOOS=$(TARGETOS) GOARCH=$(TARGETARCH) go build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/breakfix-server ./cmd/server
	CGO_ENABLED=0 GOOS=$(TARGETOS) GOARCH=$(TARGETARCH) go build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/breakfix-controller ./cmd/controller
	CGO_ENABLED=0 GOOS=$(TARGETOS) GOARCH=$(TARGETARCH) go build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/breakfix-generate-worker ./cmd/generate-worker
	CGO_ENABLED=0 GOOS=$(TARGETOS) GOARCH=$(TARGETARCH) go build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/breakfix-taxonomy-worker ./cmd/taxonomy-worker

images: build
	docker build --platform $(TARGETOS)/$(TARGETARCH) --provenance=false -t $(RUNTIME_IMAGE_REPOSITORY)/breakfix-server:$(RUNTIME_IMAGE_TAG) -f build/images/server/Dockerfile $(BIN_DIR)
	docker build --platform $(TARGETOS)/$(TARGETARCH) --provenance=false -t $(RUNTIME_IMAGE_REPOSITORY)/breakfix-controller:$(RUNTIME_IMAGE_TAG) -f build/images/controller/Dockerfile $(BIN_DIR)
	docker build --platform $(TARGETOS)/$(TARGETARCH) --provenance=false -t $(RUNTIME_IMAGE_REPOSITORY)/breakfix-generate-worker:$(RUNTIME_IMAGE_TAG) -f build/images/generate-worker/Dockerfile $(BIN_DIR)
	docker build --platform $(TARGETOS)/$(TARGETARCH) --provenance=false -t $(RUNTIME_IMAGE_REPOSITORY)/breakfix-taxonomy-worker:$(RUNTIME_IMAGE_TAG) -f build/images/taxonomy-worker/Dockerfile $(BIN_DIR)
	docker build --platform $(TARGETOS)/$(TARGETARCH) --provenance=false -t breakfix-k8s-base:latest build/images/k8s-base

deploy-kind: images
	./scripts/kind/runtime.sh

reset-kind:
	./scripts/kind/reset-state.sh

test-unit:
	go test -count=1 ./...

catalog-package:
	go run ./cmd/catalog-release -source "$(CATALOG_SOURCE)" -output "$(CATALOG_ARCHIVE)" $(if $(CATALOG_REFERENCE),-reference "$(CATALOG_REFERENCE)") $(if $(CATALOG_REGISTRY_ENDPOINT),-registry-endpoint "$(CATALOG_REGISTRY_ENDPOINT)") $(if $(CATALOG_TRUST_BUNDLE_FILE),-trust-bundle-file "$(CATALOG_TRUST_BUNDLE_FILE)")

catalog-install:
	@test -n "$(CATALOG_BUNDLE)" || { echo "CATALOG_BUNDLE must be an immutable OCI digest reference"; exit 2; }
	@test -n "$(CATALOG_ADMIN_TOKEN)" || { echo "CATALOG_ADMIN_TOKEN or BREAKFIX_CATALOG_ADMIN_TOKEN is required"; exit 2; }
	curl --fail --show-error --silent \
		-H "X-Breakfix-Catalog-Token: $(CATALOG_ADMIN_TOKEN)" \
		-H 'Content-Type: application/json' \
		--data '{"bundle":"$(CATALOG_BUNDLE)"}' \
		"$(CATALOG_SERVER_URL)/api/admin/catalog/releases"

test-e2e:
	npm ci --prefix test
	npm run test:e2e --prefix test
