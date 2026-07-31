.PHONY: dev dev-up dev-down dev-reset dev-status dev-config frontend-build \
	dev-build dev-build-server dev-build-controller dev-build-agent-worker \
	dev-build-builder dev-build-publisher dev-build-verifier dev-worker-configs \
	dev-start-server dev-start-controller dev-start-agent-worker \
	dev-start-builder dev-start-publisher dev-start-verifier \
	dev-server dev-controller dev-agent-worker dev-builder dev-publisher dev-verifier \
	telepresence-connect telepresence-server telepresence-controller \
	telepresence-agent-worker telepresence-builder telepresence-publisher telepresence-verifier \
	telepresence-down telepresence-status telepresence-disconnect \
	e2e e2e-runtime-workflow e2e-runtime-browser e2e-agent-assistant e2e-agent-soak \
	e2e-agent-node e2e-agent-k8s e2e-taxonomy e2e-server-recovery \
	dev-data dev-crd dev-rbac dev-images k8s-base-image \
	dev-incus dev-incus-catalog dev-incus-secrets dev-kind-push-k8s-base dev-kind-catalog dev-kind-runtime \
	generate-crd verify-crd-generated generate-api generate-api-go generate-api-frontend verify-api-generated \
	build build-server build-controller build-agent-worker build-builder build-publisher build-verifier \
	runtime-images runtime-push release-manifest lint clean

# ── Build info ──

VERSION   ?= 0.1.0
BUILD_TIME = $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
COMMIT     = $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)

LDFLAGS   := -s -w \
  -X 'github.com/breakfix/breakfix/internal/build.Version=$(VERSION)' \
  -X 'github.com/breakfix/breakfix/internal/build.BuildTime=$(BUILD_TIME)' \
  -X 'github.com/breakfix/breakfix/internal/build.Commit=$(COMMIT)'

CONTROLLER_GEN_VERSION := v0.21.0
CONTROLLER_GEN := go run sigs.k8s.io/controller-tools/cmd/controller-gen@$(CONTROLLER_GEN_VERSION)
CRD_TYPES_DIR := internal/k8s/apis/breakfix/v1
OAPI_CODEGEN_VERSION := v2.7.1
OAPI_CODEGEN := go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@$(OAPI_CODEGEN_VERSION)
OPENAPI_SPEC := api/openapi.yaml
OPENAPI_GO_CONFIG := api/cfg.yaml
OPENAPI_GO_OUTPUT := internal/api/server.gen.go
OPENAPI_FRONTEND_OUTPUT := frontend/src/api/generated
OPENAPI_TS := npm exec --prefix frontend -- openapi-ts
OPENAPI_TS_ARGS := -i $(CURDIR)/$(OPENAPI_SPEC) -p @hey-api/typescript --no-log-file

KIND_CLUSTER ?= breakfix-dev
CONFIG_DIR ?= config
CONFIG ?= $(CONFIG_DIR)/breakfix.yaml
DEV_CONFIG ?= $(CONFIG_DIR)/breakfix.local.yaml
DEV_RUN_DIR ?= .local/dev
DEV_AGENT_WORKER_CONFIG := $(DEV_RUN_DIR)/agent-worker.yaml
DEV_BUILDER_CONFIG := $(DEV_RUN_DIR)/builder.yaml
DEV_PUBLISHER_CONFIG := $(DEV_RUN_DIR)/publisher.yaml
DEV_VERIFIER_CONFIG := $(DEV_RUN_DIR)/verifier.yaml
DEV_AGENT_WORKER_API_KEY ?= breakfix-dev-agent-worker-key
DEV_BUILDER_WORKER_API_KEY ?= breakfix-dev-builder-worker-key
DEV_PUBLISHER_WORKER_API_KEY ?= breakfix-dev-publisher-worker-key
DEV_VERIFIER_WORKER_API_KEY ?= breakfix-dev-verifier-worker-key
BIN_DIR  := bin
TARGETOS ?= linux
TARGETARCH ?= amd64
RELEASE_DIR := $(BIN_DIR)/release/$(TARGETOS)-$(TARGETARCH)
SERVER_RELEASE_DIR := $(RELEASE_DIR)/server
CONTROLLER_RELEASE_DIR := $(RELEASE_DIR)/controller
AGENT_WORKER_RELEASE_DIR := $(RELEASE_DIR)/agent-worker
VERIFIER_RELEASE_DIR := $(RELEASE_DIR)/verifier
BUILDER_RELEASE_DIR := $(RELEASE_DIR)/builder
PUBLISHER_RELEASE_DIR := $(RELEASE_DIR)/publisher
SERVER_RELEASE_BIN := $(SERVER_RELEASE_DIR)/breakfix-server
CONTROLLER_RELEASE_BIN := $(CONTROLLER_RELEASE_DIR)/breakfix-controller
AGENT_WORKER_RELEASE_BIN := $(AGENT_WORKER_RELEASE_DIR)/breakfix-agent-worker
VERIFIER_RELEASE_BIN := $(VERIFIER_RELEASE_DIR)/breakfix-verifier
BUILDER_RELEASE_BIN := $(BUILDER_RELEASE_DIR)/breakfix-builder
PUBLISHER_RELEASE_BIN := $(PUBLISHER_RELEASE_DIR)/breakfix-publisher
RUNTIME_IMAGE_REPOSITORY ?= ghcr.io/breakfix
RUNTIME_IMAGE_TAG ?= dev
RELEASE_SOURCE_IMAGE_REPOSITORY ?= ghcr.io/breakfix
RELEASE_MANIFEST ?= dist/breakfix-$(RUNTIME_IMAGE_TAG).yaml
TELEPRESENCE ?= ./dev/telepresence.sh

# ═══════════════════════════════════════════════════════════════
# Dev build (bin/ — fast, no LDFLAGS)
# ═══════════════════════════════════════════════════════════════

frontend-build:
	npm ci --prefix frontend
	npm run build --prefix frontend

dev-build-server: frontend-build
	go build -o $(BIN_DIR)/breakfix-server ./cmd/server
	@echo "  ✓ server"

dev-build-controller:
	go build -o $(BIN_DIR)/breakfix-controller ./cmd/controller
	@echo "  ✓ controller"

dev-build-agent-worker:
	go build -o $(BIN_DIR)/breakfix-agent-worker ./cmd/agent-worker
	@echo "  ✓ agent worker"

dev-build-builder:
	go build -o $(BIN_DIR)/breakfix-builder ./cmd/builder
	@echo "  ✓ builder"

dev-build-publisher:
	go build -o $(BIN_DIR)/breakfix-publisher ./cmd/publisher
	@echo "  ✓ publisher"

dev-build-verifier:
	go build -o $(BIN_DIR)/breakfix-verifier ./cmd/verifier
	@echo "  ✓ verifier"

dev-build: dev-build-server dev-build-controller dev-build-agent-worker dev-build-builder dev-build-publisher dev-build-verifier
	@echo "  ✓ Binaries built"

# ── Dev lifecycle ──

dev-start-controller:
	@test -f $(DEV_CONFIG) || { echo "  ✗ Missing $(DEV_CONFIG). Copy $(CONFIG_DIR)/breakfix.example.yaml to $(CONFIG) and run make dev-config."; exit 1; }
	@lsof -ti:8081 | xargs kill -9 2>/dev/null || true
	@sleep 1
	@find /tmp/breakfix-controller.log /tmp/breakfix-controller.pid -depth -delete 2>/dev/null || true
	@nohup $(BIN_DIR)/breakfix-controller -config $(DEV_CONFIG) >/tmp/breakfix-controller.log 2>&1 </dev/null & echo $$! >/tmp/breakfix-controller.pid
	@sleep 3
	@pid=$$(cat /tmp/breakfix-controller.pid 2>/dev/null); \
	[ -n "$$pid" ] && kill -0 "$$pid" 2>/dev/null || { echo "  ✗ Controller failed to stay up"; tail -20 /tmp/breakfix-controller.log; exit 1; }
	@curl -fsS http://localhost:8081/healthz >/dev/null || { echo "  ✗ Controller health check failed"; tail -20 /tmp/breakfix-controller.log; exit 1; }
	@echo "  ✓ Controller :8081"

dev-start-server:
	@test -f $(DEV_CONFIG) || { echo "  ✗ Missing $(DEV_CONFIG). Copy $(CONFIG_DIR)/breakfix.example.yaml to $(CONFIG) and run make dev-config."; exit 1; }
	@lsof -ti:9090 | xargs kill -9 2>/dev/null || true
	@sleep 1
	@find /tmp/breakfix-server.log /tmp/breakfix-server.pid -depth -delete 2>/dev/null || true
	@env BREAKFIX_AGENT_WORKER_API_KEY="$(DEV_AGENT_WORKER_API_KEY)" \
		BREAKFIX_BUILDER_WORKER_API_KEY="$(DEV_BUILDER_WORKER_API_KEY)" \
		BREAKFIX_PUBLISHER_WORKER_API_KEY="$(DEV_PUBLISHER_WORKER_API_KEY)" \
		BREAKFIX_VERIFIER_WORKER_API_KEY="$(DEV_VERIFIER_WORKER_API_KEY)" \
		nohup $(BIN_DIR)/breakfix-server -config $(DEV_CONFIG) >/tmp/breakfix-server.log 2>&1 </dev/null & echo $$! >/tmp/breakfix-server.pid
	@sleep 3
	@pid=$$(cat /tmp/breakfix-server.pid 2>/dev/null); \
	[ -n "$$pid" ] && kill -0 "$$pid" 2>/dev/null || { echo "  ✗ Server failed to stay up"; tail -20 /tmp/breakfix-server.log; exit 1; }
	@curl -fsS http://localhost:9090/api/openapi.json >/dev/null || { echo "  ✗ Server HTTP check failed"; tail -20 /tmp/breakfix-server.log; exit 1; }
	@echo "  ✓ Server :9090"

dev-worker-configs: dev-config
	@mkdir -p $(DEV_RUN_DIR)
	@sed 's/^health_port:.*/health_port: 18082/' $(DEV_CONFIG) > $(DEV_AGENT_WORKER_CONFIG)
	@sed 's/^health_port:.*/health_port: 18083/' $(DEV_CONFIG) > $(DEV_BUILDER_CONFIG)
	@sed 's/^health_port:.*/health_port: 18084/' $(DEV_CONFIG) > $(DEV_PUBLISHER_CONFIG)
	@sed 's/^health_port:.*/health_port: 18085/' $(DEV_CONFIG) > $(DEV_VERIFIER_CONFIG)

dev-start-agent-worker: dev-worker-configs
	@find /tmp/breakfix-agent-worker.log /tmp/breakfix-agent-worker.pid -depth -delete 2>/dev/null || true
	@env BREAKFIX_WORKER_API_KEY="$(DEV_AGENT_WORKER_API_KEY)" nohup $(BIN_DIR)/breakfix-agent-worker -config $(DEV_AGENT_WORKER_CONFIG) >/tmp/breakfix-agent-worker.log 2>&1 </dev/null & echo $$! >/tmp/breakfix-agent-worker.pid
	@sleep 1
	@pid=$$(cat /tmp/breakfix-agent-worker.pid 2>/dev/null); \
	[ -n "$$pid" ] && kill -0 "$$pid" 2>/dev/null || { echo "  ✗ Agent Worker failed to stay up"; tail -20 /tmp/breakfix-agent-worker.log; exit 1; }
	@curl -fsS http://localhost:18082/healthz >/dev/null || { echo "  ✗ Agent Worker health check failed"; tail -20 /tmp/breakfix-agent-worker.log; exit 1; }
	@echo "  ✓ Agent Worker :18082"

dev-start-builder: dev-worker-configs
	@find /tmp/breakfix-builder.log /tmp/breakfix-builder.pid -depth -delete 2>/dev/null || true
	@env BREAKFIX_WORKER_API_KEY="$(DEV_BUILDER_WORKER_API_KEY)" nohup $(BIN_DIR)/breakfix-builder -config $(DEV_BUILDER_CONFIG) >/tmp/breakfix-builder.log 2>&1 </dev/null & echo $$! >/tmp/breakfix-builder.pid
	@sleep 1
	@pid=$$(cat /tmp/breakfix-builder.pid 2>/dev/null); \
	[ -n "$$pid" ] && kill -0 "$$pid" 2>/dev/null || { echo "  ✗ Builder failed to stay up"; tail -20 /tmp/breakfix-builder.log; exit 1; }
	@curl -fsS http://localhost:18083/healthz >/dev/null || { echo "  ✗ Builder health check failed"; tail -20 /tmp/breakfix-builder.log; exit 1; }
	@echo "  ✓ Builder :18083"

dev-start-publisher: dev-worker-configs
	@find /tmp/breakfix-publisher.log /tmp/breakfix-publisher.pid -depth -delete 2>/dev/null || true
	@env BREAKFIX_WORKER_API_KEY="$(DEV_PUBLISHER_WORKER_API_KEY)" nohup $(BIN_DIR)/breakfix-publisher -config $(DEV_PUBLISHER_CONFIG) >/tmp/breakfix-publisher.log 2>&1 </dev/null & echo $$! >/tmp/breakfix-publisher.pid
	@sleep 1
	@pid=$$(cat /tmp/breakfix-publisher.pid 2>/dev/null); \
	[ -n "$$pid" ] && kill -0 "$$pid" 2>/dev/null || { echo "  ✗ Publisher failed to stay up"; tail -20 /tmp/breakfix-publisher.log; exit 1; }
	@curl -fsS http://localhost:18084/healthz >/dev/null || { echo "  ✗ Publisher health check failed"; tail -20 /tmp/breakfix-publisher.log; exit 1; }
	@echo "  ✓ Publisher :18084"

dev-start-verifier: dev-worker-configs
	@find /tmp/breakfix-verifier.log /tmp/breakfix-verifier.pid -depth -delete 2>/dev/null || true
	@env BREAKFIX_WORKER_API_KEY="$(DEV_VERIFIER_WORKER_API_KEY)" nohup $(BIN_DIR)/breakfix-verifier -config $(DEV_VERIFIER_CONFIG) >/tmp/breakfix-verifier.log 2>&1 </dev/null & echo $$! >/tmp/breakfix-verifier.pid
	@sleep 1
	@pid=$$(cat /tmp/breakfix-verifier.pid 2>/dev/null); \
	[ -n "$$pid" ] && kill -0 "$$pid" 2>/dev/null || { echo "  ✗ Verifier failed to stay up"; tail -20 /tmp/breakfix-verifier.log; exit 1; }
	@curl -fsS http://localhost:18085/healthz >/dev/null || { echo "  ✗ Verifier health check failed"; tail -20 /tmp/breakfix-verifier.log; exit 1; }
	@echo "  ✓ Verifier :18085"

dev-up: dev-config dev-start-controller dev-start-server dev-start-agent-worker dev-start-builder dev-start-publisher dev-start-verifier
	@echo "  ✓ Server, Controller, and fixed Workers running"

dev-down:
	@{ [ -f /tmp/breakfix-server.pid ] && kill $$(cat /tmp/breakfix-server.pid) 2>/dev/null && find /tmp/breakfix-server.pid -depth -delete && echo "  ✓ Server stopped"; } || \
	 { lsof -ti:9090 | xargs kill 2>/dev/null && echo "  ✓ Server stopped"; } || echo "  - Server not running"
	@{ [ -f /tmp/breakfix-controller.pid ] && kill $$(cat /tmp/breakfix-controller.pid) 2>/dev/null && find /tmp/breakfix-controller.pid -depth -delete && echo "  ✓ Controller stopped"; } || \
	 { lsof -ti:8081 | xargs kill 2>/dev/null && echo "  ✓ Controller stopped"; } || echo "  - Controller not running"
	@{ [ -f /tmp/breakfix-agent-worker.pid ] && kill $$(cat /tmp/breakfix-agent-worker.pid) 2>/dev/null && find /tmp/breakfix-agent-worker.pid -depth -delete && echo "  ✓ Agent Worker stopped"; } || echo "  - Agent Worker not running"
	@{ [ -f /tmp/breakfix-builder.pid ] && kill $$(cat /tmp/breakfix-builder.pid) 2>/dev/null && find /tmp/breakfix-builder.pid -depth -delete && echo "  ✓ Builder stopped"; } || echo "  - Builder not running"
	@{ [ -f /tmp/breakfix-publisher.pid ] && kill $$(cat /tmp/breakfix-publisher.pid) 2>/dev/null && find /tmp/breakfix-publisher.pid -depth -delete && echo "  ✓ Publisher stopped"; } || echo "  - Publisher not running"
	@{ [ -f /tmp/breakfix-verifier.pid ] && kill $$(cat /tmp/breakfix-verifier.pid) 2>/dev/null && find /tmp/breakfix-verifier.pid -depth -delete && echo "  ✓ Verifier stopped"; } || echo "  - Verifier not running"
	@lsof -ti:8081 | xargs kill 2>/dev/null || true

dev-reset: dev-down
	@if [ -d data ]; then find data -mindepth 1 -maxdepth 1 ! -name challenges -exec rm -rf {} +; fi
	@mkdir -p data/challenges
	@echo "  ✓ Data directory reset"

# ── Dev shortcuts (build + restart) ──

dev-server: dev-config dev-build-server dev-start-server
dev-controller: dev-config dev-build-controller dev-start-controller
dev-agent-worker: dev-config dev-build-agent-worker dev-start-agent-worker
dev-builder: dev-config dev-build-builder dev-start-builder
dev-publisher: dev-config dev-build-publisher dev-start-publisher
dev-verifier: dev-config dev-build-verifier dev-start-verifier

# ── In-cluster local debugging (Telepresence) ──

telepresence-connect:
	$(TELEPRESENCE) connect

telepresence-server:
	$(TELEPRESENCE) server

telepresence-controller:
	$(TELEPRESENCE) controller

telepresence-agent-worker:
	$(TELEPRESENCE) agent-worker

telepresence-builder:
	$(TELEPRESENCE) builder

telepresence-publisher:
	$(TELEPRESENCE) publisher

telepresence-verifier:
	$(TELEPRESENCE) verifier

telepresence-down:
	$(TELEPRESENCE) down all

telepresence-status:
	$(TELEPRESENCE) status

telepresence-disconnect:
	$(TELEPRESENCE) disconnect

e2e-server-recovery:
	npm ci --prefix test
	npm run test:recovery --prefix test -- --workers=1

e2e-runtime-workflow:
	npm ci --prefix test
	npm run test:runtime:workflow --prefix test -- --workers=1

e2e-runtime-browser:
	npm ci --prefix test
	npm run test:runtime:browser --prefix test -- --workers=1

e2e-agent-assistant:
	npm ci --prefix test
	npm run test:agent-live:assistant --prefix test -- --workers=1

e2e-agent-soak:
	npm ci --prefix test
	npm run test:agent-live:soak --prefix test -- --workers=1

e2e-agent-node:
	npm ci --prefix test
	npm run test:agent-live:node --prefix test -- --workers=1

e2e-agent-k8s:
	npm ci --prefix test
	npm run test:agent-live:k8s --prefix test -- --workers=1

e2e-taxonomy: frontend-build
	@test -n "$$BREAKFIX_TAXONOMY_E2E_DATABASE_URL" || { echo "  ✗ BREAKFIX_TAXONOMY_E2E_DATABASE_URL is required"; exit 1; }
	@test -n "$$DEEPSEEK_API_KEY" || { echo "  ✗ DEEPSEEK_API_KEY is required"; exit 1; }
	npm ci --prefix test
	npm run test:taxonomy-live --prefix test -- --workers=1

e2e:
	npm ci --prefix test
	npm run test:e2e --prefix test
# ── Dev environment ──

dev-data:
	@mkdir -p data/challenges
	@echo "  ✓ Data dir ready"

generate-crd:
	$(CONTROLLER_GEN) object paths=./$(CRD_TYPES_DIR)
	$(CONTROLLER_GEN) crd:crdVersions=v1 paths=./$(CRD_TYPES_DIR) output:crd:dir=deploy/crd

verify-crd-generated:
	@tmp=$$(mktemp -d); \
	trap 'rm -rf "$$tmp"' EXIT; \
	$(CONTROLLER_GEN) object paths=./$(CRD_TYPES_DIR) output:dir=$$tmp; \
	$(CONTROLLER_GEN) crd:crdVersions=v1 paths=./$(CRD_TYPES_DIR) output:crd:dir=$$tmp/crd; \
	diff -u $(CRD_TYPES_DIR)/zz_generated.deepcopy.go $$tmp/zz_generated.deepcopy.go; \
		diff -ru --exclude=kustomization.yaml deploy/crd $$tmp/crd

generate-api-go:
	$(OAPI_CODEGEN) --config $(OPENAPI_GO_CONFIG) $(OPENAPI_SPEC)

generate-api-frontend:
	$(OPENAPI_TS) $(OPENAPI_TS_ARGS) -o $(CURDIR)/$(OPENAPI_FRONTEND_OUTPUT)

generate-api: generate-api-go generate-api-frontend

verify-api-generated:
	@tmp=$$(mktemp -d); \
	trap 'rm -rf "$$tmp"' EXIT; \
	sed "s|^output:.*|output: $$tmp/server.gen.go|" $(OPENAPI_GO_CONFIG) >"$$tmp/oapi-codegen.yaml" && \
	$(OAPI_CODEGEN) --config "$$tmp/oapi-codegen.yaml" $(OPENAPI_SPEC) && \
	$(OPENAPI_TS) $(OPENAPI_TS_ARGS) -o "$$tmp/frontend" && \
	diff -u $(OPENAPI_GO_OUTPUT) "$$tmp/server.gen.go" && \
	diff -ru $(OPENAPI_FRONTEND_OUTPUT) "$$tmp/frontend"

dev-crd: generate-crd
	@kubectl apply -f deploy/crd/breakfix.dev_nodeenvironments.yaml >/dev/null
	@kubectl apply -f deploy/crd/breakfix.dev_vk8senvironments.yaml >/dev/null
	@echo "  ✓ CRDs applied"

dev-rbac:
	@kubectl create namespace breakfix-system --dry-run=client -o yaml | kubectl apply -f - >/dev/null
	@kubectl -n breakfix-system apply -f deploy/runtime/worker-rbac.yaml >/dev/null
	@secret_type="$$(kubectl -n breakfix-system get secret breakfix-registry-pull -o jsonpath='{.type}' 2>/dev/null || true)"; \
		{ [ -z "$$secret_type" ] || [ "$$secret_type" = 'kubernetes.io/dockerconfigjson' ]; } || \
		{ echo "  ✗ Docker config Secret breakfix-registry-pull has the wrong type."; exit 1; }
	@echo "  ✓ RBAC applied"

dev-incus:
	./dev/incus-bootstrap.sh

dev-incus-catalog: dev-incus
	./dev/incus-catalog.sh

dev-incus-secrets: dev-incus
	@for role in server controller builder publisher verifier; do \
		kubectl -n breakfix-system create secret generic breakfix-incus-$$role \
			--from-file=server.crt=.local/incus/$$role/server.crt \
			--from-file=client.crt=.local/incus/$$role/client.crt \
			--from-file=client.key=.local/incus/$$role/client.key \
			--dry-run=client -o yaml | kubectl apply -f - >/dev/null; \
	done
	@set -eu; \
		remote="$${BREAKFIX_INCUS_REMOTE:-incus-cluster}"; \
		image_project="$${BREAKFIX_INCUS_IMAGE_PROJECT:-breakfix-images}"; \
		base_alias="$${BREAKFIX_INCUS_BASE_ALIAS:-node-systemd-base-v1}"; \
		runtime_secret="$${BREAKFIX_RUNTIME_SECRET:-breakfix-runtime}"; \
		fingerprint="$$(incus image list "$$remote:" "$$base_alias" --project "$$image_project" --format csv,noheader --columns F)"; \
		case "$$fingerprint" in \
		  [0-9a-f][0-9a-f]* ) [ $${#fingerprint} -eq 64 ] ;; \
		  * ) echo "  ✗ unable to resolve full Incus base fingerprint for $$base_alias"; exit 1 ;; \
		esac; \
		encoded="$$(printf '%s' "$$fingerprint" | base64 | tr -d '\n')"; \
		patch="$$(jq -cn --arg fingerprint "$$encoded" '{data: {incus_base_image_fingerprint: $$fingerprint}}')"; \
		kubectl -n breakfix-system patch secret "$$runtime_secret" --type merge --patch "$$patch" >/dev/null; \
		printf '  ✓ Role-specific Incus Secrets applied; base fingerprint recorded: %s\n' "$$fingerprint"

dev-kind-push-k8s-base:
	@set -eu; \
		namespace="$${BREAKFIX_NAMESPACE:-breakfix-system}"; \
		runtime_secret="$${BREAKFIX_RUNTIME_SECRET:-breakfix-runtime}"; \
		registry_addr="$$(kubectl -n "$$namespace" get secret "$$runtime_secret" -o json | jq -r '.data.registry_addr // "" | @base64d')"; \
		[ -n "$$registry_addr" ] || { echo "  ✗ $$runtime_secret must provide registry_addr"; exit 1; }; \
		reference="$$(./dev/kind-push-image.sh breakfix-k8s-base:latest "$$registry_addr/k8s-base:latest")"; \
		encoded="$$(printf '%s' "$$reference" | base64 | tr -d '\n')"; \
		patch="$$(jq -cn --arg digest "$$encoded" '{data: {k8s_base_image_digest: $$digest}}')"; \
		kubectl -n "$$namespace" patch secret "$$runtime_secret" --type merge --patch "$$patch" >/dev/null; \
		printf '  ✓ K8s base digest recorded: %s\n' "$$reference"

dev-kind-catalog: dev-incus-catalog
	./dev/kind-catalog.sh

dev-kind-runtime:
	./dev/kind-runtime.sh

dev-kind-reset-state:
	./dev/kind-reset-state.sh

dev-images:
	@$(MAKE) --no-print-directory k8s-base-image
	@echo "  ✓ Platform base images"

dev: dev-config dev-data dev-crd dev-rbac dev-images dev-build dev-up dev-status
	@echo ""
	@echo "══════════════════════════════════════"
	@echo "  Breakfix dev environment ready"
	@echo "══════════════════════════════════════"

dev-status:
	@echo "  Web UI:"
	@echo "    http://localhost:9090"

# ═══════════════════════════════════════════════════════════════
# Dev config
# ═══════════════════════════════════════════════════════════════

dev-config:
	@mkdir -p $(dir $(DEV_CONFIG))
	@sed \
		-e 's|^data_dir:.*|data_dir: ./data|' \
		-e "s|^kubeconfig:.*|kubeconfig: $${HOME}/.kube/config|" \
		$(CONFIG) > $(DEV_CONFIG)
	@echo "  ✓ $(DEV_CONFIG) generated"

# ═══════════════════════════════════════════════════════════════
# Production build (bin/release/<os>-<arch>/ — stripped, with LDFLAGS)
# ═══════════════════════════════════════════════════════════════

build-server: frontend-build
	@mkdir -p $(SERVER_RELEASE_DIR)
	CGO_ENABLED=0 GOOS=$(TARGETOS) GOARCH=$(TARGETARCH) go build -trimpath -ldflags "$(LDFLAGS)" -o $(SERVER_RELEASE_BIN) ./cmd/server
	@echo "  ✓ Server binary"

build-controller:
	@mkdir -p $(CONTROLLER_RELEASE_DIR)
	CGO_ENABLED=0 GOOS=$(TARGETOS) GOARCH=$(TARGETARCH) go build -trimpath -ldflags "$(LDFLAGS)" -o $(CONTROLLER_RELEASE_BIN) ./cmd/controller
	@echo "  ✓ Controller binary"

build-agent-worker:
	@mkdir -p $(AGENT_WORKER_RELEASE_DIR)
	CGO_ENABLED=0 GOOS=$(TARGETOS) GOARCH=$(TARGETARCH) go build -trimpath -ldflags "$(LDFLAGS)" -o $(AGENT_WORKER_RELEASE_BIN) ./cmd/agent-worker
	@echo "  ✓ Agent Worker binary"

build-verifier:
	@mkdir -p $(VERIFIER_RELEASE_DIR)
	CGO_ENABLED=0 GOOS=$(TARGETOS) GOARCH=$(TARGETARCH) go build -trimpath -ldflags "$(LDFLAGS)" -o $(VERIFIER_RELEASE_BIN) ./cmd/verifier
	@echo "  ✓ Verifier binary"

build-builder:
	@mkdir -p $(BUILDER_RELEASE_DIR)
	CGO_ENABLED=0 GOOS=$(TARGETOS) GOARCH=$(TARGETARCH) go build -trimpath -ldflags "$(LDFLAGS)" -o $(BUILDER_RELEASE_BIN) ./cmd/builder
	@echo "  ✓ Builder binary"

build-publisher:
	@mkdir -p $(PUBLISHER_RELEASE_DIR)
	CGO_ENABLED=0 GOOS=$(TARGETOS) GOARCH=$(TARGETARCH) go build -trimpath -ldflags "$(LDFLAGS)" -o $(PUBLISHER_RELEASE_BIN) ./cmd/publisher
	@echo "  ✓ Publisher binary"

build: build-server build-controller build-agent-worker build-builder build-publisher build-verifier
	@echo "  ✓ Production binaries built"

runtime-images: build-server build-controller build-agent-worker build-builder build-publisher build-verifier
	docker build --platform $(TARGETOS)/$(TARGETARCH) --provenance=false -t $(RUNTIME_IMAGE_REPOSITORY)/breakfix-server:$(RUNTIME_IMAGE_TAG) -f deploy/images/server/Dockerfile $(SERVER_RELEASE_DIR)
	docker build --platform $(TARGETOS)/$(TARGETARCH) --provenance=false -t $(RUNTIME_IMAGE_REPOSITORY)/breakfix-controller:$(RUNTIME_IMAGE_TAG) -f deploy/images/controller/Dockerfile $(CONTROLLER_RELEASE_DIR)
	docker build --platform $(TARGETOS)/$(TARGETARCH) --provenance=false -t $(RUNTIME_IMAGE_REPOSITORY)/breakfix-agent-worker:$(RUNTIME_IMAGE_TAG) -f deploy/images/agent-worker/Dockerfile $(AGENT_WORKER_RELEASE_DIR)
	docker build --platform $(TARGETOS)/$(TARGETARCH) --provenance=false -t $(RUNTIME_IMAGE_REPOSITORY)/breakfix-builder:$(RUNTIME_IMAGE_TAG) -f deploy/images/builder/Dockerfile $(BUILDER_RELEASE_DIR)
	docker build --platform $(TARGETOS)/$(TARGETARCH) --provenance=false -t $(RUNTIME_IMAGE_REPOSITORY)/breakfix-publisher:$(RUNTIME_IMAGE_TAG) -f deploy/images/publisher/Dockerfile $(PUBLISHER_RELEASE_DIR)
	docker build --platform $(TARGETOS)/$(TARGETARCH) --provenance=false -t $(RUNTIME_IMAGE_REPOSITORY)/breakfix-verifier:$(RUNTIME_IMAGE_TAG) -f deploy/images/verifier/Dockerfile $(VERIFIER_RELEASE_DIR)
	@echo "  ✓ Runtime images built"

runtime-push: runtime-images
	docker push $(RUNTIME_IMAGE_REPOSITORY)/breakfix-server:$(RUNTIME_IMAGE_TAG)
	docker push $(RUNTIME_IMAGE_REPOSITORY)/breakfix-controller:$(RUNTIME_IMAGE_TAG)
	docker push $(RUNTIME_IMAGE_REPOSITORY)/breakfix-agent-worker:$(RUNTIME_IMAGE_TAG)
	docker push $(RUNTIME_IMAGE_REPOSITORY)/breakfix-builder:$(RUNTIME_IMAGE_TAG)
	docker push $(RUNTIME_IMAGE_REPOSITORY)/breakfix-publisher:$(RUNTIME_IMAGE_TAG)
	docker push $(RUNTIME_IMAGE_REPOSITORY)/breakfix-verifier:$(RUNTIME_IMAGE_TAG)
	@echo "  ✓ Runtime images pushed"

# release-manifest renders the canonical deployment package after pushing all
# six runtime images and replaces every mutable development reference with the
# Registry-resolved immutable digest. The resulting YAML is the release's
# deployable artifact, including Builder/Publisher/Verifier image settings
# embedded in the in-cluster ConfigMap.
release-manifest: runtime-push
	@test "$(RUNTIME_IMAGE_TAG)" != "dev" || { echo "  ✗ RUNTIME_IMAGE_TAG must be an immutable release tag"; exit 1; }
	@mkdir -p $(dir $(RELEASE_MANIFEST))
	kubectl kustomize . > $(RELEASE_MANIFEST)
	@set -eu; \
	for component in server controller agent-worker builder publisher verifier; do \
		ref="$(RUNTIME_IMAGE_REPOSITORY)/breakfix-$$component:$(RUNTIME_IMAGE_TAG)"; \
		digest="$$(docker buildx imagetools inspect "$$ref" --format '{{.Manifest.Digest}}')"; \
		[ -n "$$digest" ] || { echo "  ✗ resolve digest for $$ref"; exit 1; }; \
		sed -i "s|$(RELEASE_SOURCE_IMAGE_REPOSITORY)/breakfix-$$component:dev|$(RUNTIME_IMAGE_REPOSITORY)/breakfix-$$component@$$digest|g" $(RELEASE_MANIFEST); \
	done
	@! rg -n '$(RELEASE_SOURCE_IMAGE_REPOSITORY)/breakfix-(server|controller|agent-worker|builder|publisher|verifier):dev' $(RELEASE_MANIFEST)
	@echo "  ✓ $(RELEASE_MANIFEST)"

# ═══════════════════════════════════════════════════════════════
# Platform images (local dev)
# ═══════════════════════════════════════════════════════════════

k8s-base-image:
	docker build --platform linux/amd64 --provenance=false -t breakfix-k8s-base:latest ./deploy/images/k8s-base
	@$(MAKE) --no-print-directory dev-kind-push-k8s-base
	@echo "  ✓ breakfix-k8s-base published to the configured OCI Registry"

# ═══════════════════════════════════════════════════════════════
# Ops
# ═══════════════════════════════════════════════════════════════

lint:
	golangci-lint run ./...

clean:
	rm -rf $(BIN_DIR)/ dist/
	@echo "  ✓ Cleaned"
