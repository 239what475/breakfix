.PHONY: dev dev-up dev-down dev-reset dev-status dev-config frontend-build \
	dev-build dev-build-server dev-build-controller dev-build-agent-worker \
	dev-start-server dev-start-controller dev-start-agent-worker \
	dev-server dev-controller dev-agent-worker \
	telepresence-connect telepresence-server telepresence-controller telepresence-worker telepresence-down telepresence-status telepresence-disconnect \
        e2e \
		e2e-runtime-verify \
		verify-challenge \
		e2e-builder-boundary \
		e2e-runtime-browser \
		e2e-agent-assistant \
		e2e-agent-soak \
		e2e-agent-container \
		e2e-agent-vcluster \
		e2e-taxonomy \
        e2e-server-recovery \
        dev-registry dev-data dev-crd dev-rbac dev-images docker-base \
        generate-crd verify-crd-generated generate-api generate-api-go generate-api-frontend verify-api-generated \
	build build-server build-controller build-agent-worker build-builder build-publisher build-verifier runtime-images runtime-push release-manifest \
	verification-images \
        lint proto clean

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

REGISTRY    ?= localhost:5000
ACR_NS      ?= break-fix
KIND_CLUSTER ?= breakfix-dev
NETWORK_POLICY_KIND_CLUSTER ?= breakfix-network-policy-e2e
NETWORK_POLICY_KUBECTL_CONTEXT ?= kind-$(NETWORK_POLICY_KIND_CLUSTER)
CILIUM_CHART_VERSION ?= 1.19.6

CONFIG_DIR ?= config
CONFIG ?= $(CONFIG_DIR)/breakfix.yaml
DEV_CONFIG ?= $(CONFIG_DIR)/breakfix.local.yaml
DEV_IMAGE_PREFIX ?= $(shell awk '/^registry_addr:/{print $$2}' $(DEV_CONFIG) 2>/dev/null)
ifeq ($(strip $(DEV_IMAGE_PREFIX)),)
DEV_IMAGE_PREFIX := $(REGISTRY)/$(ACR_NS)
endif

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

dev-build: dev-build-server dev-build-controller dev-build-agent-worker
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
	@nohup $(BIN_DIR)/breakfix-server -config $(DEV_CONFIG) >/tmp/breakfix-server.log 2>&1 </dev/null & echo $$! >/tmp/breakfix-server.pid
	@sleep 3
	@pid=$$(cat /tmp/breakfix-server.pid 2>/dev/null); \
	[ -n "$$pid" ] && kill -0 "$$pid" 2>/dev/null || { echo "  ✗ Server failed to stay up"; tail -20 /tmp/breakfix-server.log; exit 1; }
	@curl -fsS http://localhost:9090/api/openapi.json >/dev/null || { echo "  ✗ Server HTTP check failed"; tail -20 /tmp/breakfix-server.log; exit 1; }
	@echo "  ✓ Server :9090"

dev-start-agent-worker:
	@test -f $(DEV_CONFIG) || { echo "  ✗ Missing $(DEV_CONFIG). Copy $(CONFIG_DIR)/breakfix.example.yaml to $(CONFIG) and run make dev-config."; exit 1; }
	@find /tmp/breakfix-agent-worker.log /tmp/breakfix-agent-worker.pid -depth -delete 2>/dev/null || true
	@nohup $(BIN_DIR)/breakfix-agent-worker -config $(DEV_CONFIG) >/tmp/breakfix-agent-worker.log 2>&1 </dev/null & echo $$! >/tmp/breakfix-agent-worker.pid
	@sleep 1
	@pid=$$(cat /tmp/breakfix-agent-worker.pid 2>/dev/null); \
	[ -n "$$pid" ] && kill -0 "$$pid" 2>/dev/null || { echo "  ✗ Agent Worker failed to stay up"; tail -20 /tmp/breakfix-agent-worker.log; exit 1; }
	@echo "  ✓ Agent Worker"

dev-up: dev-config dev-start-controller dev-start-server dev-start-agent-worker
	@echo "  ✓ Server and Controller running"

dev-down:
	@{ [ -f /tmp/breakfix-server.pid ] && kill $$(cat /tmp/breakfix-server.pid) 2>/dev/null && find /tmp/breakfix-server.pid -depth -delete && echo "  ✓ Server stopped"; } || \
	 { lsof -ti:9090 | xargs kill 2>/dev/null && echo "  ✓ Server stopped"; } || echo "  - Server not running"
	@{ [ -f /tmp/breakfix-controller.pid ] && kill $$(cat /tmp/breakfix-controller.pid) 2>/dev/null && find /tmp/breakfix-controller.pid -depth -delete && echo "  ✓ Controller stopped"; } || \
	 { lsof -ti:8081 | xargs kill 2>/dev/null && echo "  ✓ Controller stopped"; } || echo "  - Controller not running"
	@{ [ -f /tmp/breakfix-agent-worker.pid ] && kill $$(cat /tmp/breakfix-agent-worker.pid) 2>/dev/null && find /tmp/breakfix-agent-worker.pid -depth -delete && echo "  ✓ Agent Worker stopped"; } || echo "  - Agent Worker not running"
	@lsof -ti:8081 | xargs kill 2>/dev/null || true
	@docker stop registry 2>/dev/null && echo "  ✓ Registry stopped" || echo "  - Registry not running"

dev-reset: dev-down
	@if [ -d data ]; then find data -mindepth 1 -maxdepth 1 ! -name challenges -exec rm -rf {} +; fi
	@mkdir -p data/challenges
	@echo "  ✓ Data directory reset"

# ── Dev shortcuts (build + restart) ──

dev-server: dev-config dev-build-server dev-start-server
dev-controller: dev-config dev-build-controller dev-start-controller
dev-agent-worker: dev-config dev-build-agent-worker dev-start-agent-worker

# ── In-cluster local debugging (Telepresence) ──

telepresence-connect:
	$(TELEPRESENCE) connect

telepresence-server:
	$(TELEPRESENCE) server

telepresence-controller:
	$(TELEPRESENCE) controller

telepresence-worker:
	$(TELEPRESENCE) worker

telepresence-down:
	$(TELEPRESENCE) down all

telepresence-status:
	$(TELEPRESENCE) status

telepresence-disconnect:
	$(TELEPRESENCE) disconnect

e2e-server-recovery:
	npm ci --prefix test
	npm run test:recovery --prefix test -- --workers=1

e2e-runtime-verify:
	npm ci --prefix test
	npm run test:runtime:verify --prefix test -- --workers=1

verify-challenge:
	@test -n "$(NAME)" || { echo "  ERROR: NAME is required, for example: make verify-challenge NAME=container-runtime-init"; exit 1; }
	npm ci --prefix test
	VERIFY_CHALLENGE_NAME="$(NAME)" npm run test:runtime:challenge --prefix test -- --workers=1 --reporter=list

# This suite needs a CNI that actually enforces Kubernetes NetworkPolicy.
# Kind's default kindnet does not, so the isolated cluster is created with its
# CNI disabled and Cilium is installed exactly as documented by Cilium.
e2e-builder-boundary:
	@if ! kind get clusters | rg -qx '$(NETWORK_POLICY_KIND_CLUSTER)'; then \
		kind create cluster --name $(NETWORK_POLICY_KIND_CLUSTER) --config test/kind/network-policy-kind.yaml; \
	fi
	helm upgrade --install cilium oci://quay.io/cilium/charts/cilium \
		--version $(CILIUM_CHART_VERSION) \
		--namespace kube-system \
		--kube-context $(NETWORK_POLICY_KUBECTL_CONTEXT) \
		--set image.pullPolicy=IfNotPresent \
		--set operator.replicas=1 \
		--set ipam.mode=kubernetes
	kubectl --context $(NETWORK_POLICY_KUBECTL_CONTEXT) -n kube-system rollout status daemonset/cilium --timeout=5m
	kubectl --context $(NETWORK_POLICY_KUBECTL_CONTEXT) -n kube-system rollout status deployment/cilium-operator --timeout=5m
	@$(MAKE) --no-print-directory verification-images KIND_CLUSTER=$(NETWORK_POLICY_KIND_CLUSTER)
	npm ci --prefix test
	BREAKFIX_RUNTIME_KUBECTL_CONTEXT=$(NETWORK_POLICY_KUBECTL_CONTEXT) \
		npm run test:runtime:builder-boundary --prefix test -- --workers=1

e2e-runtime-browser:
	npm ci --prefix test
	npm run test:runtime:browser --prefix test -- --workers=1

e2e-agent-assistant:
	npm ci --prefix test
	npm run test:agent-live:assistant --prefix test -- --workers=1

e2e-agent-soak:
	npm ci --prefix test
	npm run test:agent-live:soak --prefix test -- --workers=1

e2e-agent-container:
	npm ci --prefix test
	npm run test:agent-live:container --prefix test -- --workers=1

e2e-agent-vcluster:
	npm ci --prefix test
	npm run test:agent-live:vcluster --prefix test -- --workers=1

e2e-taxonomy: frontend-build
	@test -n "$$BREAKFIX_TAXONOMY_E2E_DATABASE_URL" || { echo "  ✗ BREAKFIX_TAXONOMY_E2E_DATABASE_URL is required"; exit 1; }
	@test -n "$$DEEPSEEK_API_KEY" || { echo "  ✗ DEEPSEEK_API_KEY is required"; exit 1; }
	npm ci --prefix test
	npm run test:taxonomy-live --prefix test -- --workers=1

e2e:
	npm ci --prefix test
	npm run test:e2e --prefix test
# ── Dev environment ──

dev-registry:
	@if docker inspect registry >/dev/null 2>&1; then \
		if docker inspect registry --format '{{range .Config.Env}}{{println .}}{{end}}' | grep -qx 'REGISTRY_STORAGE_DELETE_ENABLED=true'; then \
			docker start registry 2>/dev/null || true; \
		else \
			volume=$$(docker inspect registry --format '{{range .Mounts}}{{if eq .Destination "/var/lib/registry"}}{{.Source}}{{end}}{{end}}'); \
			[ -n "$$volume" ] || { echo "ERROR: registry data volume is missing"; exit 1; }; \
			docker rm -f registry >/dev/null; \
			docker run -d -p 5000:5000 --name registry -e REGISTRY_STORAGE_DELETE_ENABLED=true -v "$$volume:/var/lib/registry" registry:2 >/dev/null; \
		fi; \
	else \
		docker run -d -p 5000:5000 --name registry -e REGISTRY_STORAGE_DELETE_ENABLED=true registry:2 >/dev/null; \
	fi
	@echo "  ✓ Registry :5000 (manifest deletion enabled)"

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
	diff -ru deploy/crd $$tmp/crd

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
	@kubectl apply -f deploy/crd/breakfix.dev_containerenvironments.yaml >/dev/null
	@kubectl apply -f deploy/crd/breakfix.dev_vclusterenvironments.yaml >/dev/null
	@kubectl apply -f deploy/crd/breakfix.dev_verifytasks.yaml >/dev/null
	@echo "  ✓ CRDs applied"

dev-rbac:
	@kubectl create namespace breakfix-system --dry-run=client -o yaml | kubectl apply -f - >/dev/null
	@kubectl apply -f deploy/rbac/verifier.yaml >/dev/null
	@kubectl -n breakfix-system create secret generic breakfix-registry-pull \
		--type=kubernetes.io/dockerconfigjson \
		--from-literal=.dockerconfigjson='{"auths":{}}' \
		--dry-run=client -o yaml | kubectl apply -f - >/dev/null
	@echo "  ✓ RBAC applied"

dev-images:
	@$(MAKE) --no-print-directory docker-base
	@$(MAKE) --no-print-directory verification-images
	@for d in data/challenges/*/; do \
		name=$$(basename $$d); \
		[ "$$name" = "base" ] && continue; \
		$(MAKE) --no-print-directory docker-challenge NAME=$$name; \
	done
	@echo "  ✓ Challenge images"

dev: dev-config dev-registry dev-data dev-crd dev-rbac dev-images dev-build dev-up dev-status
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

dev-config: $(DEV_CONFIG)

$(DEV_CONFIG): $(CONFIG)
	@mkdir -p $(dir $@)
	@sed \
		-e 's|^data_dir:.*|data_dir: ./data|' \
		-e "s|^kubeconfig:.*|kubeconfig: $${HOME}/.kube/config|" \
		-e 's|^registry_addr:.*|registry_addr: 172.18.0.1:5000/break-fix|' \
		-e 's|^registry_insecure:.*|registry_insecure: true|' \
		$< > $@
	@echo "  ✓ $@ generated"

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
# Docker images (local dev)
# ═══════════════════════════════════════════════════════════════

docker-base:
	docker build --platform linux/amd64 --provenance=false -t breakfix-base:latest ./deploy/images/base
	docker tag breakfix-base:latest $(REGISTRY)/$(ACR_NS)/breakfix-base:latest
	docker tag breakfix-base:latest $(DEV_IMAGE_PREFIX)/breakfix-base:latest
	docker push $(REGISTRY)/$(ACR_NS)/breakfix-base:latest
	kind load docker-image breakfix-base:latest --name $(KIND_CLUSTER)
	kind load docker-image $(REGISTRY)/$(ACR_NS)/breakfix-base:latest --name $(KIND_CLUSTER)
	kind load docker-image $(DEV_IMAGE_PREFIX)/breakfix-base:latest --name $(KIND_CLUSTER)
	docker build --platform linux/amd64 --provenance=false -t breakfix-k8s-base:latest ./deploy/images/k8s-base
	docker tag breakfix-k8s-base:latest $(REGISTRY)/$(ACR_NS)/breakfix-k8s-base:latest
	docker tag breakfix-k8s-base:latest $(DEV_IMAGE_PREFIX)/breakfix-k8s-base:latest
	docker push $(REGISTRY)/$(ACR_NS)/breakfix-k8s-base:latest
	kind load docker-image breakfix-k8s-base:latest --name $(KIND_CLUSTER)
	kind load docker-image $(REGISTRY)/$(ACR_NS)/breakfix-k8s-base:latest --name $(KIND_CLUSTER)
	kind load docker-image $(DEV_IMAGE_PREFIX)/breakfix-k8s-base:latest --name $(KIND_CLUSTER)
	@echo "  ✓ breakfix-base + breakfix-k8s-base → registry + Kind"

docker-challenge:
	docker build -t $(NAME):v1 ./data/challenges/$(NAME)
	docker tag $(NAME):v1 $(REGISTRY)/$(ACR_NS)/$(NAME):v1
	docker tag $(NAME):v1 $(DEV_IMAGE_PREFIX)/$(NAME):v1
	docker push $(REGISTRY)/$(ACR_NS)/$(NAME):v1
	kind load docker-image $(NAME):v1 --name $(KIND_CLUSTER)
	kind load docker-image $(REGISTRY)/$(ACR_NS)/$(NAME):v1 --name $(KIND_CLUSTER)
	kind load docker-image $(DEV_IMAGE_PREFIX)/$(NAME):v1 --name $(KIND_CLUSTER)
	@echo "  ✓ $(NAME) → registry + Kind"

docker-push:
	docker build -t $(NAME):v1 ./data/challenges/$(NAME)
	docker tag $(NAME):v1 $(REGISTRY)/$(ACR_NS)/$(NAME):v1
	docker push $(REGISTRY)/$(ACR_NS)/$(NAME):v1
	@echo "  ✓ $(NAME) → $(REGISTRY)"

verification-images:
	@$(MAKE) --no-print-directory build-builder build-publisher build-verifier
	docker build --platform $(TARGETOS)/$(TARGETARCH) --provenance=false -t breakfix-builder:latest -f deploy/images/builder/Dockerfile $(BUILDER_RELEASE_DIR)
	docker build --platform $(TARGETOS)/$(TARGETARCH) --provenance=false -t breakfix-publisher:latest -f deploy/images/publisher/Dockerfile $(PUBLISHER_RELEASE_DIR)
	docker build --platform $(TARGETOS)/$(TARGETARCH) --provenance=false -t breakfix-verifier:latest -f deploy/images/verifier/Dockerfile $(VERIFIER_RELEASE_DIR)
	kind load docker-image breakfix-builder:latest --name $(KIND_CLUSTER)
	kind load docker-image breakfix-publisher:latest --name $(KIND_CLUSTER)
	kind load docker-image breakfix-verifier:latest --name $(KIND_CLUSTER)
	@echo "  ✓ Builder, Publisher, and Verifier images built and loaded into Kind"

# ═══════════════════════════════════════════════════════════════
# Ops
# ═══════════════════════════════════════════════════════════════

lint:
	golangci-lint run ./...

proto:
	docker run --rm -v $(CURDIR):/workspace -w /workspace namely/protoc:latest \
		--proto_path=/workspace/proto \
		--go_out=/workspace/pkg/proto --go_opt=paths=source_relative \
		--go-grpc_out=/workspace/pkg/proto --go-grpc_opt=paths=source_relative \
		/workspace/proto/breakfix.proto
	docker run --rm -v $(CURDIR):/workspace alpine chown -R 1000:1000 /workspace/pkg/proto/

clean:
	rm -rf $(BIN_DIR)/ dist/
	@echo "  ✓ Cleaned"
