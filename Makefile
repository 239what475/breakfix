.PHONY: build build-server build-cli \
        deploy deploy-server deploy-image deploy-images deploy-cleanup deploy-reset \
        dev dev-build dev-server dev-cli dev-down dev-reset \
        lint proto clean status logs \
        docker-challenge docker-push

# ── Build info ──

VERSION   ?= 0.1.0
BUILD_TIME = $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
COMMIT     = $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)

LDFLAGS   := -s -w \
  -X 'github.com/breakfix/breakfix/internal/build.Version=$(VERSION)' \
  -X 'github.com/breakfix/breakfix/internal/build.BuildTime=$(BUILD_TIME)' \
  -X 'github.com/breakfix/breakfix/internal/build.Commit=$(COMMIT)'

# ── Server / Docker / Kind ──

SERVER      ?= $(shell cat .breakfix-server 2>/dev/null || echo "")
SERVER_BIN  ?= /usr/local/bin/breakfix-api
SERVER_CONF ?= /var/lib/breakfix/breakfix.yaml
SERVER_DATA ?= /var/lib/breakfix
SERVICE     ?= breakfix-api
CLI_BIN     ?= /usr/local/bin/breakfix
REGISTRY    ?= localhost:5000
ACR_NS      ?= break-fix
KIND_CLUSTER ?= breakfix-dev

# ═══════════════════════════════════════════════════════════════
# Production build
# ═══════════════════════════════════════════════════════════════

build-server:
	go build -ldflags "$(LDFLAGS)" -o dist/breakfix-api-linux-amd64 ./cmd/server

build-cli:
	go build -ldflags "$(LDFLAGS)" -o dist/breakfix-cli-linux-amd64 ./cmd/cli

build: build-server build-cli

# ═══════════════════════════════════════════════════════════════
# Remote deploy
# ═══════════════════════════════════════════════════════════════

_guard-server:
	@if [ -z "$(SERVER)" ]; then \
		echo "ERROR: SERVER not set."; \
		echo "  echo myserver2 > .breakfix-server"; \
		echo "  or: make $@ SERVER=<host>"; \
		exit 1; \
	fi

deploy: deploy-images deploy-server

deploy-server: build-server _guard-server
	scp dist/breakfix-api-linux-amd64 $(SERVER):/tmp/breakfix-api
	ssh $(SERVER) 'sudo mv /tmp/breakfix-api $(SERVER_BIN) && sudo systemctl restart $(SERVICE)'
	@echo "✓ Server deployed to $(SERVER) and restarted"

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
		echo "  ✗ Registry not configured (or localhost). Skipping $(NAME)."; \
		exit 0; \
	fi; \
	pub=$$(echo "$$vpc" | sed 's/-vpc//'); \
	echo "  Registry: $$pub"; \
	acr_ns=$$(grep '^acr_namespace:' breakfix.yaml | sed 's/^acr_namespace: *//'); \
	docker build -t $(NAME):v1 ./challenges/$(NAME); \
	docker tag $(NAME):v1 $$pub/$$acr_ns/$(NAME):v1; \
	docker push $$pub/$$acr_ns/$(NAME):v1
	@echo "  ✓ $(NAME) → ACR"

deploy-cleanup: _guard-server
	@ssh $(SERVER) 'kubectl get ns -o name 2>/dev/null | grep "^namespace/break" | sed "s|^namespace/||" | xargs -r kubectl delete ns --wait=false' || true
	@echo "✓ K8s namespaces cleaned up"

deploy-reset: deploy-cleanup _guard-server
	@ssh $(SERVER) 'sudo rm -f $(SERVER_DATA)/breakfix.db* && sudo systemctl restart $(SERVICE)'
	@echo "✓ Remote reset complete (DB cleared, CA regenerated)"

# ═══════════════════════════════════════════════════════════════
# Local dev
# ═══════════════════════════════════════════════════════════════

dev: dev-registry dev-data dev-images dev-build dev-start dev-status
	@echo ""
	@echo "══════════════════════════════════════"
	@echo "  Breakfix dev environment ready"
	@echo "══════════════════════════════════════"

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

dev-images:
	@for d in challenges/*/; do \
		name=$$(basename $$d); \
		[ "$$name" = "base" ] && continue; \
		$(MAKE) --no-print-directory docker-challenge NAME=$$name; \
	done
	@echo "  ✓ Challenge images"

dev-build:
	@[ -f breakfix.yaml ] || { \
		echo "ERROR: breakfix.yaml not found."; \
		echo "Run: cp breakfix.example.yaml breakfix.yaml"; \
		exit 1; \
	}
	@sed \
		-e 's|^data_dir:.*|data_dir: ./data|' \
		-e "s|^kubeconfig:.*|kubeconfig: $${HOME}/.kube/config|" \
		-e 's|^registry:.*|registry: localhost:5000|' \
		breakfix.yaml > breakfix-local.yaml
	go build -o bin/breakfix-api ./cmd/server
	go build -o bin/breakfix-cli ./cmd/cli
	@echo "  ✓ Binaries built"

dev-start:
	@pkill breakfix-api 2>/dev/null || true
	@sleep 0.5
	@./bin/breakfix-api -config breakfix-local.yaml >/tmp/breakfix-api.log 2>&1 &
	@sleep 2
	@grep -q "listening" /tmp/breakfix-api.log || { \
		echo "  ✗ Server failed to start"; tail -5 /tmp/breakfix-api.log; exit 1; \
	}
	@echo "  ✓ Server listening on :9090 :9533"

dev-status:
	@echo "  ✓ Proxy    :3128"
	@echo ""
	@echo "  CLI:"
	@echo "    ./bin/breakfix-cli register -u <user> -p <pass>"
	@echo "    ./bin/breakfix-cli login    -u <user> -p <pass> -t <totp>"
	@echo "    ./bin/breakfix-cli list"
	@echo "    ./bin/breakfix-cli start cleanup-logs"

dev-server:
	go build -o bin/breakfix-api ./cmd/server
	@pkill breakfix-api 2>/dev/null || true
	@sleep 0.5
	@./bin/breakfix-api -config breakfix-local.yaml >/tmp/breakfix-api.log 2>&1 &
	@sleep 2
	@grep -q "listening" /tmp/breakfix-api.log || { \
		echo "  ✗ Server failed to start"; tail -5 /tmp/breakfix-api.log; exit 1; \
	}
	@echo "  ✓ Server rebuilt and restarted"

dev-cli:
	go build -o bin/breakfix-cli ./cmd/cli
	@echo "  ✓ CLI rebuilt"

dev-down:
	@pkill breakfix-api 2>/dev/null && echo "  ✓ Server stopped" || echo "  - Server not running"
	@docker stop registry 2>/dev/null && echo "  ✓ Registry stopped" || echo "  - Registry not running"

dev-reset: dev-down
	@rm -rf data/
	@echo "  ✓ Data directory reset"

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
# Lint / Proto / Ops
# ═══════════════════════════════════════════════════════════════

lint:
	golangci-lint run ./...

proto:
	docker run --rm -v $(CURDIR):/workspace -w /workspace namely/protoc:latest \
		--proto_path=/workspace/proto \
		--go_out=/workspace/internal/proto --go_opt=paths=source_relative \
		--go-grpc_out=/workspace/internal/proto --go-grpc_opt=paths=source_relative \
		/workspace/proto/breakfix.proto
	docker run --rm -v $(CURDIR):/workspace alpine chown -R 1000:1000 /workspace/internal/proto/

status: _guard-server
	ssh $(SERVER) 'sudo systemctl status $(SERVICE) --no-pager'

logs: _guard-server
	ssh $(SERVER) 'sudo journalctl -u $(SERVICE) -f'

# ═══════════════════════════════════════════════════════════════
# Clean
# ═══════════════════════════════════════════════════════════════

clean:
	rm -rf bin/ dist/
