.PHONY: dev dev-up dev-down dev-reset dev-status \
        dev-build dev-build-gateway dev-build-controller dev-build-cli \
        dev-start-gateway dev-start-controller \
        dev-gateway dev-controller dev-cli \
        dev-registry dev-data dev-crd dev-rbac dev-images \
        build build-gateway build-controller build-cli \
        deploy deploy-gateway deploy-controller deploy-images deploy-image deploy-config deploy-generator deploy-cleanup deploy-reset \
        generator-build generator-dev generator-run \
        lint proto clean status logs

# ── Build info ──

VERSION   ?= 0.1.0
BUILD_TIME = $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
COMMIT     = $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)

LDFLAGS   := -s -w \
  -X 'github.com/breakfix/breakfix/internal/build.Version=$(VERSION)' \
  -X 'github.com/breakfix/breakfix/internal/build.BuildTime=$(BUILD_TIME)' \
  -X 'github.com/breakfix/breakfix/internal/build.Commit=$(COMMIT)'

# ── Remote server ──

SERVER      ?= $(shell cat .breakfix-server 2>/dev/null || echo "")
SERVER_BIN  ?= /usr/local/bin/breakfix-gateway
SERVER_CONF ?= /var/lib/breakfix/breakfix.yaml
SERVER_DATA ?= /var/lib/breakfix
SERVICE     ?= breakfix-gateway
REGISTRY    ?= localhost:5000
ACR_NS      ?= break-fix
KIND_CLUSTER ?= breakfix-dev

BIN_DIR  := bin
DIST_DIR := dist

# ═══════════════════════════════════════════════════════════════
# Dev build (bin/ — fast, no LDFLAGS)
# ═══════════════════════════════════════════════════════════════

dev-build-gateway:
	go build -o $(BIN_DIR)/breakfix-gateway ./cmd/gateway
	@echo "  ✓ gateway"

dev-build-controller:
	go build -o $(BIN_DIR)/breakfix-controller ./cmd/controller
	@echo "  ✓ controller"

dev-build-cli:
	go build -o $(BIN_DIR)/breakfix-cli ./cmd/cli
	@echo "  ✓ cli"

dev-build: dev-build-gateway dev-build-controller dev-build-cli
	@echo "  ✓ All binaries built"

# ── Dev lifecycle ──

dev-start-gateway:
	@lsof -ti:9090 | xargs kill -9 2>/dev/null || true
	@sleep 1
	@$(BIN_DIR)/breakfix-gateway -config breakfix-local.yaml >/tmp/breakfix-gateway.log 2>&1 &
	@sleep 2
	@grep -q "listening" /tmp/breakfix-gateway.log || { echo "  ✗ Gateway failed to start"; tail -5 /tmp/breakfix-gateway.log; exit 1; }
	@echo "  ✓ Gateway :9090"

dev-start-controller:
	@lsof -ti:8081 | xargs kill -9 2>/dev/null || true
	@sleep 1
	@$(BIN_DIR)/breakfix-controller -config breakfix-local.yaml >/tmp/breakfix-controller.log 2>&1 &
	@sleep 2
	@echo "  ✓ Controller"

dev-up: dev-start-gateway dev-start-controller
	@echo "  ✓ All services running"

dev-down:
	@lsof -ti:9090 | xargs kill 2>/dev/null && echo "  ✓ Gateway stopped" || echo "  - Gateway not running"
	@lsof -ti:8081 | xargs kill 2>/dev/null && echo "  ✓ Controller stopped" || echo "  - Controller not running"
	@docker stop registry 2>/dev/null && echo "  ✓ Registry stopped" || echo "  - Registry not running"

dev-reset: dev-down
	@rm -rf data/
	@echo "  ✓ Data directory reset"

# ── Dev shortcuts (build + restart) ──

dev-gateway: dev-build-gateway dev-start-gateway
dev-controller: dev-build-controller dev-start-controller
dev-cli: dev-build-cli

# ── Dev environment ──

dev-registry:
	@if docker inspect registry >/dev/null 2>&1; then \
		docker start registry 2>/dev/null || true; \
	else \
		docker run -d -p 5000:5000 --name registry registry:2; \
	fi
	@echo "  ✓ Registry :5000"

dev-data:
	@mkdir -p data
	@[ -L data/challenges ] || ln -s ../challenges data/challenges
	@echo "  ✓ Data dir ready"

dev-crd:
	@kubectl apply -f config/crd/breakfix.dev_generations.yaml >/dev/null 2>&1 || true
	@kubectl apply -f config/crd/breakfix.dev_instances.yaml >/dev/null 2>&1 || true
	@echo "  ✓ CRDs applied"

dev-rbac:
	@kubectl apply -f config/rbac/controller.yaml >/dev/null 2>&1 || true
	@echo "  ✓ RBAC applied"

dev-images:
	@for d in challenges/*/; do \
		name=$$(basename $$d); \
		[ "$$name" = "base" ] && continue; \
		$(MAKE) --no-print-directory docker-challenge NAME=$$name; \
	done
	@echo "  ✓ Challenge images"

dev: dev-registry dev-data dev-crd dev-rbac dev-images dev-build dev-up dev-status
	@echo ""
	@echo "══════════════════════════════════════"
	@echo "  Breakfix dev environment ready"
	@echo "══════════════════════════════════════"

dev-status:
	@echo "  ✓ Proxy    :3128"
	@echo ""
	@echo "  CLI:"
	@echo "    ./bin/breakfix-cli register -u <user> -p <pass>"
	@echo "    ./bin/breakfix-cli login    -u <user> -p <pass> -t <totp>"
	@echo "    ./bin/breakfix-cli list"
	@echo "    ./bin/breakfix-cli start cleanup-logs"

# ═══════════════════════════════════════════════════════════════
# Dev config (generated from breakfix.yaml)
# ═══════════════════════════════════════════════════════════════

breakfix-local.yaml: breakfix.yaml
	@sed \
		-e 's|^data_dir:.*|data_dir: ./data|' \
		-e "s|^kubeconfig:.*|kubeconfig: $${HOME}/.kube/config|" \
		-e 's|^registry:.*|registry: localhost:5000|' \
		$< > $@
	@echo "  ✓ $@ generated"

# ═══════════════════════════════════════════════════════════════
# Production build (dist/ — stripped, with LDFLAGS)
# ═══════════════════════════════════════════════════════════════

build-gateway:
	go build -ldflags "$(LDFLAGS)" -o $(DIST_DIR)/breakfix-gateway-linux-amd64 ./cmd/gateway
	@echo "  ✓ Gateway binary"

build-controller:
	go build -ldflags "$(LDFLAGS)" -o $(DIST_DIR)/breakfix-controller-linux-amd64 ./cmd/controller
	@echo "  ✓ Controller binary"

build-cli:
	go build -ldflags "$(LDFLAGS)" -o $(DIST_DIR)/breakfix-cli-linux-amd64 ./cmd/cli
	@echo "  ✓ CLI binary"

build: build-gateway build-controller build-cli
	@echo "  ✓ All production binaries built"

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

deploy-gateway: build-gateway _guard-server
	scp $(DIST_DIR)/breakfix-gateway-linux-amd64 $(SERVER):/tmp/breakfix-gateway
	ssh $(SERVER) 'sudo mv /tmp/breakfix-gateway $(SERVER_BIN) && sudo systemctl restart $(SERVICE)'
	@echo "✓ Gateway deployed and restarted"

deploy-controller: build-controller _guard-server
	scp $(DIST_DIR)/breakfix-controller-linux-amd64 $(SERVER):/tmp/breakfix-controller
	ssh $(SERVER) 'sudo mv /tmp/breakfix-controller /usr/local/bin/breakfix-controller && sudo systemctl restart breakfix-controller'
	@echo "✓ Controller deployed and restarted"

deploy-config: _guard-server
	scp breakfix.yaml $(SERVER):/tmp/breakfix.yaml
	ssh $(SERVER) 'sudo mv /tmp/breakfix.yaml $(SERVER_CONF) && sudo chown breakfix:breakfix $(SERVER_CONF) && sudo systemctl restart $(SERVICE) breakfix-controller'
	@echo "✓ Config deployed, services restarted"

deploy-images: _guard-server
	@for d in challenges/*/; do \
		name=$$(basename $$d); \
		[ "$$name" = "base" ] && continue; \
		$(MAKE) --no-print-directory deploy-image NAME=$$name; \
	done
	@echo "✓ All images deployed"

deploy-image: _guard-server
	@[ -f breakfix.yaml ] || { echo "ERROR: breakfix.yaml not found."; exit 1; }
	@vpc=$$(grep '^registry:' breakfix.yaml | sed 's/^registry: *//'); \
	if [ -z "$$vpc" ] || [ "$$vpc" = "localhost:5000" ]; then \
		echo "  ✗ Registry not configured. Skipping $(NAME)."; \
		exit 0; \
	fi; \
	pub=$$(echo "$$vpc" | sed 's/-vpc//'); \
	acr_ns=$$(grep '^acr_namespace:' breakfix.yaml | sed 's/^acr_namespace: *//'); \
	docker build -t $(NAME):v1 ./challenges/$(NAME); \
	docker tag $(NAME):v1 $$pub/$$acr_ns/$(NAME):v1; \
	docker push $$pub/$$acr_ns/$(NAME):v1
	@echo "  ✓ $(NAME) → ACR"

deploy-cleanup: _guard-server
	@ssh $(SERVER) 'kubectl get ns -o name 2>/dev/null | grep "^namespace/break" | sed "s|^namespace/||" | xargs -r kubectl delete ns --wait=false' || true
	@echo "✓ K8s namespaces cleaned up"

deploy-reset: deploy-cleanup _guard-server
	@ssh $(SERVER) 'sudo rm -f $(SERVER_DATA)/breakfix.db* && sudo systemctl restart $(SERVICE) breakfix-controller'
	@echo "✓ Remote reset complete (DB cleared, CA regenerated)"

deploy: deploy-generator deploy-images deploy-gateway deploy-controller

# ═══════════════════════════════════════════════════════════════
# Docker images (local dev)
# ═══════════════════════════════════════════════════════════════

docker-challenge:
	docker build -t $(NAME):v1 ./challenges/$(NAME)
	docker tag $(NAME):v1 $(REGISTRY)/$(ACR_NS)/$(NAME):v1
	docker push $(REGISTRY)/$(ACR_NS)/$(NAME):v1
	kind load docker-image $(REGISTRY)/$(ACR_NS)/$(NAME):v1 --name $(KIND_CLUSTER)
	@echo "  ✓ $(NAME) → registry + Kind"

docker-push:
	docker build -t $(NAME):v1 ./challenges/$(NAME)
	docker tag $(NAME):v1 $(REGISTRY)/$(ACR_NS)/$(NAME):v1
	docker push $(REGISTRY)/$(ACR_NS)/$(NAME):v1
	@echo "  ✓ $(NAME) → $(REGISTRY)"

# ═══════════════════════════════════════════════════════════════
# Generator
# ═══════════════════════════════════════════════════════════════

generator-build:
	CGO_ENABLED=0 go build -ldflags "-s -w" -o $(BIN_DIR)/generator ./cmd/generator
	docker build -t breakfix-generator:latest -f Dockerfile.generator .
	kind load docker-image breakfix-generator:latest --name $(KIND_CLUSTER)
	@echo "  ✓ Generator image built and loaded into Kind"

generator-dev: dev-rbac generator-build dev-gateway dev-controller
	@kubectl delete jobs -n breakfix-system --all 2>/dev/null || true
	@kubectl delete pods -n breakfix-system --all 2>/dev/null || true
	@echo "  ✓ Generator dev environment ready"

generator-run:
	./$(BIN_DIR)/breakfix-cli generate --topic "$(TOPIC)"

deploy-generator: _guard-server generator-build
	@vpc=$$(grep '^registry:' breakfix.yaml | sed 's/^registry: *//'); \
	if [ -z "$$vpc" ] || [ "$$vpc" = "localhost:5000" ]; then \
		echo "  ✗ Registry not configured. Skipping."; exit 1; \
	fi; \
	pub=$$(echo "$$vpc" | sed 's/-vpc//'); \
	acr_ns=$$(grep '^acr_namespace:' breakfix.yaml | sed 's/^acr_namespace: *//'); \
	docker tag breakfix-generator:latest $$pub/$$acr_ns/breakfix-generator:latest; \
	docker push $$pub/$$acr_ns/breakfix-generator:latest
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
	ssh $(SERVER) 'sudo systemctl status $(SERVICE) --no-pager'

logs: _guard-server
	ssh $(SERVER) 'sudo journalctl -u $(SERVICE) -f'

clean:
	rm -rf $(BIN_DIR)/ $(DIST_DIR)/
	@echo "  ✓ Cleaned"
