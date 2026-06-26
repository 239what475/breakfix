.PHONY: dev-up dev-down dev-server dev-cli dev prod lint kind-up kind-down run clean proto certs

LDFLAGS = -ldflags "\
  -X 'github.com/breakfix/breakfix/internal/build.Version=0.1.0' \
  -X 'github.com/breakfix/breakfix/internal/build.BuildTime=$(shell date -u +%Y-%m-%dT%H:%M:%SZ)' \
  -X 'github.com/breakfix/breakfix/internal/build.Commit=$(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)'"

# ── Local dev environment ──

dev-up:
	docker compose -f docker-compose.dev.yml up -d
	@echo "Teleport starting at https://localhost:3080"
	@echo "Run 'make dev-setup' to configure users and certs"

dev-setup:
	@# Create dev user (invitation token)
	docker compose -f docker-compose.dev.yml exec -T teleport tctl users add dev-user --roles=access 2>&1 | tee /dev/stderr | grep 'https://' || true
	@# Export Teleport TLS user CA (for client cert verification)
	docker compose -f docker-compose.dev.yml exec -T teleport tctl auth export --type=tls-user > dev/certs/teleport-ca.pem
	@# Generate dev CA for server cert
	openssl req -x509 -newkey rsa:2048 -nodes \
		-keyout dev/certs/server-ca-key.pem \
		-out dev/certs/server-ca.pem \
		-subj "/CN=breakfix-dev-ca" -days 365 2>/dev/null
	@# Generate server cert signed by dev CA
	openssl req -new -newkey rsa:2048 -nodes \
		-keyout dev/certs/server-key.pem \
		-out dev/certs/server.csr \
		-subj "/CN=localhost" 2>/dev/null
		@echo "subjectAltName=DNS:localhost,IP:127.0.0.1" > dev/certs/ext.cnf
	openssl x509 -req -in dev/certs/server.csr \
		-CA dev/certs/server-ca.pem -CAkey dev/certs/server-ca-key.pem \
		-CAcreateserial -extfile dev/certs/ext.cnf -out dev/certs/server-cert.pem -days 365 2>/dev/null
	@rm -f dev/certs/server.csr dev/certs/server-ca-key.pem dev/certs/server-ca.srl dev/certs/ext.cnf
	@echo ""
	@echo "=== Setup complete ==="
	@echo "Certs ready in dev/certs/"
	@echo "  teleport-ca.pem  → verify client (from Teleport)"
	@echo "  server-ca.pem    → verify server (dev CA for CLI)"
	@echo "  server-cert.pem  → API Server TLS cert"
	@echo "  server-key.pem   → API Server TLS key"
	@echo ""
	@echo "Open the URL above in browser → create password → scan TOTP QR code"
	@echo "Then: tsh login --proxy=localhost:3080 --user=dev-user"

dev-down:
	docker compose -f docker-compose.dev.yml down

# ── Build ──

dev-server:
	go build $(LDFLAGS) -o bin/breakfix-api ./cmd/server

dev-cli:
	go build $(LDFLAGS) -o bin/breakfix-cli ./cmd/cli

dev: dev-server dev-cli

prod:
	go build $(LDFLAGS) -o bin/breakfix-api ./cmd/server
	go build $(LDFLAGS) -o bin/breakfix-cli ./cmd/cli

# ── Lint ──

lint:
	golangci-lint run ./...

# ── Kind ──

kind-up:
	kind create cluster --name breakfix-dev

kind-down:
	kind delete cluster --name breakfix-dev

# ── Docker images ──

docker-base:
	docker build -t breakfix-base:latest ./base
	kind load docker-image breakfix-base:latest --name breakfix-dev

docker-challenge:
	docker build -t breakfix-$(NAME):dev ./challenges/$(NAME)
	kind load docker-image breakfix-$(NAME):dev --name breakfix-dev

# ── Run ──

run-server:
	./bin/breakfix-api \
		--port=9090 \
		--db=breakfix.db \
		--challenges=./challenges \
		--cert=dev/certs/server-cert.pem \
		--key=dev/certs/server-key.pem \
		--ca=dev/certs/teleport-ca.pem

run-cli:
	./bin/breakfix-cli \
		--server=localhost:9090 \
		--cert=$$(ls ~/.tsh/keys/localhost/dev-user | head -1) \
		--key=$$(ls ~/.tsh/keys/localhost/dev-user | head -1) \
		--ca=dev/certs/server-ca.pem

# ── Clean ──

clean:
	rm -rf bin/ dev/certs/server-*.pem dev/certs/server.csr

# ── Proto ──

proto:
	docker run --rm -v $(CURDIR):/workspace -w /workspace namely/protoc:latest \
		--proto_path=/workspace/proto \
		--go_out=/workspace/internal/proto --go_opt=paths=source_relative \
		--go-grpc_out=/workspace/internal/proto --go-grpc_opt=paths=source_relative \
		/workspace/proto/breakfix.proto
	docker run --rm -v $(CURDIR):/workspace alpine chown -R 1000:1000 /workspace/internal/proto/
