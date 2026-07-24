.PHONY: dev dev-up dev-down dev-reset dev-status dev-config frontend-build \
        dev-build dev-build-server dev-build-controller \
        dev-start-server dev-start-controller \
        dev-server dev-controller \
        e2e \
        e2e-server-recovery \
        dev-registry dev-data dev-crd dev-rbac dev-images docker-base \
        generate-crd verify-crd-generated generate-api verify-api-generated \
        build build-server build-controller \
        deploy deploy-server deploy-controller deploy-images deploy-image deploy-base deploy-generator deploy-catalog deploy-cleanup deploy-reset \
        generator-build generator-dev \
        lint proto clean status logs

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

# ── Remote server ──

SERVER      ?= $(shell cat .breakfix-server 2>/dev/null || echo "")
SERVER_BIN  ?= /usr/local/bin/breakfix-server
CONTROLLER_BIN ?= /usr/local/bin/breakfix-controller
SERVER_DATA ?= /var/lib/breakfix
SERVER_SERVICE ?= breakfix-server
CONTROLLER_SERVICE ?= breakfix-controller
REGISTRY    ?= localhost:5000
ACR_NS      ?= break-fix
KIND_CLUSTER ?= breakfix-dev

CONFIG_DIR ?= config
CONFIG ?= $(CONFIG_DIR)/breakfix.yaml
DEV_CONFIG ?= $(CONFIG_DIR)/breakfix.local.yaml
DEV_IMAGE_PREFIX ?= $(shell awk '/^registry_addr:/{print $$2}' $(DEV_CONFIG) 2>/dev/null)
ifeq ($(strip $(DEV_IMAGE_PREFIX)),)
DEV_IMAGE_PREFIX := $(REGISTRY)/$(ACR_NS)
endif

BIN_DIR  := bin
DIST_DIR := dist

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

dev-build: dev-build-server dev-build-controller
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

dev-up: dev-config dev-start-controller dev-start-server
	@echo "  ✓ Server and Controller running"

dev-down:
	@{ [ -f /tmp/breakfix-server.pid ] && kill $$(cat /tmp/breakfix-server.pid) 2>/dev/null && find /tmp/breakfix-server.pid -depth -delete && echo "  ✓ Server stopped"; } || \
	 { lsof -ti:9090 | xargs kill 2>/dev/null && echo "  ✓ Server stopped"; } || echo "  - Server not running"
	@{ [ -f /tmp/breakfix-controller.pid ] && kill $$(cat /tmp/breakfix-controller.pid) 2>/dev/null && find /tmp/breakfix-controller.pid -depth -delete && echo "  ✓ Controller stopped"; } || \
	 { lsof -ti:8081 | xargs kill 2>/dev/null && echo "  ✓ Controller stopped"; } || echo "  - Controller not running"
	@lsof -ti:8081 | xargs kill 2>/dev/null || true
	@docker stop registry 2>/dev/null && echo "  ✓ Registry stopped" || echo "  - Registry not running"

dev-reset: dev-down
	@if [ -d data ]; then find data -mindepth 1 -maxdepth 1 ! -name challenges -exec rm -rf {} +; fi
	@mkdir -p data/challenges
	@echo "  ✓ Data directory reset"

# ── Dev shortcuts (build + restart) ──

dev-server: dev-config dev-build-server dev-start-server
dev-controller: dev-config dev-build-controller dev-start-controller

e2e-server-recovery:
	npm ci --prefix test
	RUN_SERVER_RECOVERY_E2E=1 npm run test:e2e --prefix test -- --workers=1 --grep 'server restart leaves controller reconciliation active|controller restart reconciles an existing environment'

e2e:
	npm ci --prefix test
	npm run test:e2e --prefix test
# ── Dev environment ──

dev-registry:
	@if docker inspect registry >/dev/null 2>&1; then \
		docker start registry 2>/dev/null || true; \
	else \
		docker run -d -p 5000:5000 --name registry registry:2; \
	fi
	@echo "  ✓ Registry :5000"

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

generate-api:
	npm run generate:api --prefix frontend

verify-api-generated:
	@$(MAKE) --no-print-directory generate-api
	@git diff --exit-code -- frontend/src/api/generated

dev-crd: generate-crd
	@kubectl apply -f deploy/crd/breakfix.dev_generations.yaml >/dev/null
	@kubectl apply -f deploy/crd/breakfix.dev_containerenvironments.yaml >/dev/null
	@kubectl apply -f deploy/crd/breakfix.dev_vclusterenvironments.yaml >/dev/null
	@kubectl apply -f deploy/crd/breakfix.dev_verifytasks.yaml >/dev/null
	@echo "  ✓ CRDs applied"

dev-rbac:
	@kubectl apply -f deploy/rbac/controller.yaml >/dev/null
	@echo "  ✓ RBAC applied"

dev-images:
	@$(MAKE) --no-print-directory docker-base
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
	@echo "  ✓ Proxy    :3128"
	@echo ""
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
# Production build (dist/ — stripped, with LDFLAGS)
# ═══════════════════════════════════════════════════════════════

build-server: frontend-build
	go build -ldflags "$(LDFLAGS)" -o $(DIST_DIR)/breakfix-server-linux-amd64 ./cmd/server
	@echo "  ✓ Server binary"

build-controller:
	go build -ldflags "$(LDFLAGS)" -o $(DIST_DIR)/breakfix-controller-linux-amd64 ./cmd/controller
	@echo "  ✓ Controller binary"

build: build-server build-controller
	@echo "  ✓ Production binaries built"

# ═══════════════════════════════════════════════════════════════
# Remote deploy
# ═══════════════════════════════════════════════════════════════

_guard-server:
	@if [ -z "$(SERVER)" ]; then \
		echo "ERROR: SERVER not set."; \
		echo "  echo myserver > .breakfix-server"; \
		echo "  or: make $@ SERVER=<host>"; \
		exit 1; \
	fi

deploy-server: build-server _guard-server
	scp $(DIST_DIR)/breakfix-server-linux-amd64 $(SERVER):/tmp/breakfix-server
	ssh $(SERVER) 'sudo mv /tmp/breakfix-server $(SERVER_BIN) && sudo systemctl restart $(SERVER_SERVICE)'
	@echo "✓ Server deployed and restarted"

deploy-controller: build-controller _guard-server
	scp $(DIST_DIR)/breakfix-controller-linux-amd64 $(SERVER):/tmp/breakfix-controller
	ssh $(SERVER) 'sudo mv /tmp/breakfix-controller $(CONTROLLER_BIN) && sudo systemctl restart $(CONTROLLER_SERVICE)'
	@echo "✓ Controller deployed and restarted"

deploy-catalog: _guard-server
	tar -C data -czf /tmp/breakfix-challenges.tar.gz challenges
	scp /tmp/breakfix-challenges.tar.gz $(SERVER):/tmp/breakfix-challenges.tar.gz
	ssh $(SERVER) 'set -e; \
		sudo rm -rf /tmp/breakfix-challenges && sudo mkdir -p /tmp/breakfix-challenges; \
		sudo tar -C /tmp/breakfix-challenges -xzf /tmp/breakfix-challenges.tar.gz; \
		sudo rm -rf $(SERVER_DATA)/challenges.tmp; \
		sudo mv /tmp/breakfix-challenges/challenges $(SERVER_DATA)/challenges.tmp; \
		sudo chown -R breakfix:breakfix $(SERVER_DATA)/challenges.tmp; \
		sudo rm -rf $(SERVER_DATA)/challenges && sudo mv $(SERVER_DATA)/challenges.tmp $(SERVER_DATA)/challenges; \
		sudo rm -rf /tmp/breakfix-challenges /tmp/breakfix-challenges.tar.gz'
	@rm -f /tmp/breakfix-challenges.tar.gz
	@echo "✓ Challenge catalog deployed"

deploy-images: _guard-server
	@$(MAKE) --no-print-directory deploy-base
	@for d in data/challenges/*/; do \
		name=$$(basename $$d); \
		[ "$$name" = "base" ] && continue; \
		$(MAKE) --no-print-directory deploy-image NAME=$$name; \
	done
	@echo "✓ All images deployed"

deploy-base: _guard-server
	@[ -f $(CONFIG) ] || { echo "ERROR: $(CONFIG) not found."; exit 1; }
	@vpc=$$(awk '/^registry_addr:/{print $$2}' $(CONFIG)); \
	if [ -z "$$vpc" ] || [ "$$vpc" = "localhost:5000/break-fix" ]; then \
		echo "  ✗ Registry not configured. Skipping breakfix-base."; \
		exit 0; \
	fi; \
	pub=$$(echo "$$vpc" | sed 's/-vpc//'); \
	docker build --platform linux/amd64 --provenance=false -t breakfix-base:latest ./deploy/images/base; \
	docker tag breakfix-base:latest $$pub/breakfix-base:latest; \
	docker push $$pub/breakfix-base:latest; \
	docker build --platform linux/amd64 --provenance=false -t breakfix-k8s-base:latest ./deploy/images/k8s-base; \
	docker tag breakfix-k8s-base:latest $$pub/breakfix-k8s-base:latest; \
	docker push $$pub/breakfix-k8s-base:latest
	@echo "  ✓ breakfix-base + breakfix-k8s-base → ACR"

deploy-image: _guard-server
	@[ -f $(CONFIG) ] || { echo "ERROR: $(CONFIG) not found."; exit 1; }
	@vpc=$$(awk '/^registry_addr:/{print $$2}' $(CONFIG)); \
	if [ -z "$$vpc" ] || [ "$$vpc" = "localhost:5000/break-fix" ]; then \
		echo "  ✗ Registry not configured. Skipping $(NAME)."; \
		exit 0; \
	fi; \
	pub=$$(echo "$$vpc" | sed 's/-vpc//'); \
	docker build -t $(NAME):v1 ./data/challenges/$(NAME); \
	docker tag $(NAME):v1 $$pub/$(NAME):v1; \
	docker push $$pub/$(NAME):v1
	@echo "  ✓ $(NAME) → ACR"

deploy-cleanup: _guard-server
	@ssh $(SERVER) 'kubectl get ns -o name 2>/dev/null | grep "^namespace/break" | sed "s|^namespace/||" | xargs -r kubectl delete ns --wait=false' || true
	@echo "✓ K8s namespaces cleaned up"

deploy-reset: deploy-cleanup _guard-server
	@ssh $(SERVER) 'sudo rm -f $(SERVER_DATA)/breakfix.db* && sudo systemctl restart $(SERVER_SERVICE)'
	@echo "✓ Remote reset complete (DB cleared, CA regenerated)"

deploy: deploy-generator deploy-images deploy-catalog deploy-controller deploy-server

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

# ═══════════════════════════════════════════════════════════════
# Generator
# ═══════════════════════════════════════════════════════════════

generator-build:
	CGO_ENABLED=0 go build -ldflags "-s -w" -o $(BIN_DIR)/generator ./cmd/generator
	docker build -t breakfix-generator:latest -f deploy/images/generator/Dockerfile .
	docker tag breakfix-generator:latest $(REGISTRY)/$(ACR_NS)/breakfix-generator:latest
	docker push $(REGISTRY)/$(ACR_NS)/breakfix-generator:latest
	kind load docker-image breakfix-generator:latest --name $(KIND_CLUSTER)
	kind load docker-image $(REGISTRY)/$(ACR_NS)/breakfix-generator:latest --name $(KIND_CLUSTER)
	@echo "  ✓ Generator image built and loaded into Kind"

generator-dev: dev-rbac generator-build dev-up
	@kubectl delete jobs -n breakfix-system --all 2>/dev/null || true
	@kubectl delete pods -n breakfix-system --all 2>/dev/null || true
	@echo "  ✓ Generator dev environment ready"

deploy-generator: _guard-server generator-build
	@[ -f $(CONFIG) ] || { echo "ERROR: $(CONFIG) not found."; exit 1; }
	@vpc=$$(awk '/^registry_addr:/{print $$2}' $(CONFIG)); \
	if [ -z "$$vpc" ] || [ "$$vpc" = "localhost:5000/break-fix" ]; then \
		echo "  ✗ Registry not configured. Skipping."; exit 1; \
	fi; \
	pub=$$(echo "$$vpc" | sed 's/-vpc//'); \
	docker tag breakfix-generator:latest $$pub/breakfix-generator:latest; \
	docker push $$pub/breakfix-generator:latest
	@echo "  ✓ Generator image pushed to ACR"

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

status: _guard-server
	ssh $(SERVER) 'sudo systemctl status $(SERVER_SERVICE) $(CONTROLLER_SERVICE) --no-pager'

logs: _guard-server
	ssh $(SERVER) 'sudo journalctl -u $(SERVER_SERVICE) -u $(CONTROLLER_SERVICE) -f'

clean:
	rm -rf $(BIN_DIR)/ $(DIST_DIR)/
	@echo "  ✓ Cleaned"
