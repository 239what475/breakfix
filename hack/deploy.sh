#!/bin/bash
set -euo pipefail

SERVER="${SERVER:-myserver2}"
SERVER_BIN="${SERVER_BIN:-/usr/local/bin/breakfix-gateway}"
SERVER_CONF="${SERVER_CONF:-/var/lib/breakfix/breakfix.yaml}"
SERVICE="${SERVICE:-breakfix-gateway}"
VERSION="${VERSION:-0.1.0}"
BUILD_TIME="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
COMMIT="$(git rev-parse --short HEAD 2>/dev/null || echo unknown)"

LDFLAGS="-s -w \
  -X 'github.com/breakfix/breakfix/internal/build.Version=${VERSION}' \
  -X 'github.com/breakfix/breakfix/internal/build.BuildTime=${BUILD_TIME}' \
  -X 'github.com/breakfix/breakfix/internal/build.Commit=${COMMIT}'"

build_frontend() {
    npm ci --prefix frontend
    npm run build --prefix frontend
}

build_server() {
    echo "=== Building server (${VERSION} ${COMMIT}) ==="
    build_frontend
    go build -ldflags "${LDFLAGS}" -o dist/breakfix-gateway-linux-amd64 ./cmd/gateway
}

deploy_server() {
    build_server
    echo "=== Deploying to ${SERVER} ==="
    scp dist/breakfix-gateway-linux-amd64 "${SERVER}:/tmp/breakfix-gateway"
    ssh "${SERVER}" "sudo mv /tmp/breakfix-gateway ${SERVER_BIN} && sudo systemctl restart ${SERVICE}"
    echo "✓ Server deployed and restarted"
    ssh "${SERVER}" "sudo systemctl status ${SERVICE} --no-pager" || true
}

case "${1:-}" in
    server)
        deploy_server
        ;;
    all)
        deploy_server
        ;;
    build-server)
        build_server
        ;;
    status)
        ssh "${SERVER}" "sudo systemctl status ${SERVICE} --no-pager"
        ;;
    logs)
        ssh "${SERVER}" "sudo journalctl -u ${SERVICE} -f"
        ;;
    config)
        echo "=== Deploying config to ${SERVER} ==="
        scp breakfix.yaml "${SERVER}:/tmp/breakfix.yaml"
        ssh "${SERVER}" "sudo mv /tmp/breakfix.yaml ${SERVER_CONF} && sudo chown breakfix:breakfix ${SERVER_CONF} && sudo systemctl restart ${SERVICE}"
        echo "✓ Config deployed and server restarted"
        ;;
    *)
        echo "Usage: $0 {server|all|config|build-server|status|logs}"
        exit 1
        ;;
esac
