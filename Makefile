.PHONY: dev-server dev-cli dev prod lint kind-up kind-down run clean

LDFLAGS_DEV = -ldflags "\
  -X 'github.com/breakfix/breakfix/internal/build.Version=0.1.0' \
  -X 'github.com/breakfix/breakfix/internal/build.BuildTime=$(shell date -u +%Y-%m-%dT%H:%M:%SZ)' \
  -X 'github.com/breakfix/breakfix/internal/build.Commit=$(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)' \
  -X 'github.com/breakfix/breakfix/internal/build.Mode=dev'"

LDFLAGS_PROD = -ldflags "\
  -X 'github.com/breakfix/breakfix/internal/build.Version=$(VER)' \
  -X 'github.com/breakfix/breakfix/internal/build.BuildTime=$(shell date -u +%Y-%m-%dT%H:%M:%SZ)' \
  -X 'github.com/breakfix/breakfix/internal/build.Commit=$(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)' \
  -X 'github.com/breakfix/breakfix/internal/build.Mode=prod'"

dev-server:
	go build $(LDFLAGS_DEV) -o bin/breakfix-api-dev ./cmd/server

dev-cli:
	go build $(LDFLAGS_DEV) -o bin/breakfix-dev ./cmd/cli

dev: dev-server dev-cli

prod:
	go build $(LDFLAGS_PROD) -o bin/breakfix-api ./cmd/server
	go build $(LDFLAGS_PROD) -o bin/breakfix-cli ./cmd/cli

lint:
	golangci-lint run ./...

kind-up:
	kind create cluster --name breakfix-dev

kind-down:
	kind delete cluster --name breakfix-dev

# Build docker images for development
docker-base:
	docker build -t breakfix-base:latest ./base
	kind load docker-image breakfix-base:latest --name breakfix-dev

docker-challenge:
	docker build -t breakfix-$(NAME):dev ./challenges/$(NAME)
	kind load docker-image breakfix-$(NAME):dev --name breakfix-dev

run: dev
	./bin/breakfix-api-dev --port 9090 --db breakfix.db --challenges ./challenges

test-flow: dev
	@echo "=== Breakfix E2E Test ==="
	./bin/breakfix-api-dev --port 9091 --db /tmp/breakfix-test.db --challenges ./challenges &
	@sleep 2
	./bin/breakfix-dev --server localhost:9091 login
	./bin/breakfix-dev --server localhost:9091 list
	@echo "=== Starting challenge ==="
	@ID=$$(./bin/breakfix-dev --server localhost:9091 start cleanup-logs 2>&1 | grep Instance | awk '{print $$3}'); \
	echo "Instance: $$ID"; \
	NS=$$(kubectl get pod -A -l instance-id=$$ID -o jsonpath='{.items[0].metadata.namespace}'); \
	POD=$$(kubectl get pod -A -l instance-id=$$ID -o jsonpath='{.items[0].metadata.name}'); \
	echo "Writing cleanup script..."; \
	kubectl exec -n $$NS $$POD -- bash -c 'echo -e "#!/bin/bash\nset -e\nmkdir -p /backup\nfind /var/log -type f -name \"*.log\" -mtime +6 -size +100M | while IFS= read -r f; do name=\$$(basename \"\$$f\"); tar -czf \"/backup/\$${name%.log}.tar.gz\" -C /var/log \"\$$name\"; done" > /usr/local/bin/cleanup.sh && chmod +x /usr/local/bin/cleanup.sh'; \
	./bin/breakfix-dev --server localhost:9091 submit $$ID
	@kill %1 2>/dev/null || true

clean:
	rm -rf bin/

proto:
	docker run --rm -v $(CURDIR):/workspace -w /workspace namely/protoc:latest \
		--proto_path=/workspace/proto \
		--go_out=/workspace/internal/proto --go_opt=paths=source_relative \
		--go-grpc_out=/workspace/internal/proto --go-grpc_opt=paths=source_relative \
		/workspace/proto/breakfix.proto
	docker run --rm -v $(CURDIR):/workspace alpine chown -R 1000:1000 /workspace/internal/proto/
