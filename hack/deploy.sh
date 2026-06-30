#!/bin/bash
set -euo pipefail

SERVER="${SERVER:-myserver2}"
SERVER_BIN="${SERVER_BIN:-/usr/local/bin/breakfix-api}"
SERVER_CONF="${SERVER_CONF:-/var/lib/breakfix/breakfix.yaml}"
SERVICE="${SERVICE:-breakfix-api}"
CLI_BIN="${CLI_BIN:-/usr/local/bin/breakfix}"
VERSION="${VERSION:-0.1.0}"
BUILD_TIME="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
COMMIT="$(git rev-parse --short HEAD 2>/dev/null || echo unknown)"

LDFLAGS="-s -w \
  -X 'github.com/breakfix/breakfix/internal/build.Version=${VERSION}' \
  -X 'github.com/breakfix/breakfix/internal/build.BuildTime=${BUILD_TIME}' \
  -X 'github.com/breakfix/breakfix/internal/build.Commit=${COMMIT}'"

build_server() {
    echo "=== Building server (${VERSION} ${COMMIT}) ==="
    go build -ldflags "${LDFLAGS}" -o dist/breakfix-api-linux-amd64 ./cmd/server
}

build_cli() {
    echo "=== Building CLI (${VERSION} ${COMMIT}) ==="
    go build -ldflags "${LDFLAGS}" -o dist/breakfix-cli-linux-amd64 ./cmd/cli
}

deploy_server() {
    build_server
    echo "=== Deploying to ${SERVER} ==="
    scp dist/breakfix-api-linux-amd64 "${SERVER}:/tmp/breakfix-api"
    ssh "${SERVER}" "sudo mv /tmp/breakfix-api ${SERVER_BIN} && sudo systemctl restart ${SERVICE}"
    echo "✓ Server deployed and restarted"
    ssh "${SERVER}" "sudo systemctl status ${SERVICE} --no-pager" || true
}

deploy_cli() {
    build_cli
    echo "=== Installing CLI ==="
    sudo cp dist/breakfix-cli-linux-amd64 "${CLI_BIN}"
    echo "✓ CLI installed to ${CLI_BIN}"
}

case "${1:-}" in
    server)
        deploy_server
        ;;
    cli)
        deploy_cli
        ;;
    all)
        deploy_server
        deploy_cli
        ;;
    build-server)
        build_server
        ;;
    build-cli)
        build_cli
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
        echo "Usage: $0 {server|cli|all|config|build-server|build-cli|status|logs}"
        exit 1
        ;;
esac
