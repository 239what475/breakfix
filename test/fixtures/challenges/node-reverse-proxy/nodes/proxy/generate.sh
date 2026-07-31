#!/bin/bash
set -euo pipefail

cat >/etc/systemd/system/breakfix-proxy.service <<'EOF'
[Unit]
Description=Breakfix fixture reverse proxy
After=network-online.target

[Service]
ExecStart=/usr/bin/socat TCP-LISTEN:8080,reuseaddr,fork TCP:app:9090
Restart=on-failure

[Install]
WantedBy=multi-user.target
EOF
systemctl daemon-reload
systemctl enable --now breakfix-proxy.service
