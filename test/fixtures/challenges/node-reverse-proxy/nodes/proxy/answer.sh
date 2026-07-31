#!/bin/bash
set -euo pipefail

sed -i 's/TCP:app:9090/TCP:app:8080/' /etc/systemd/system/breakfix-proxy.service
systemctl daemon-reload
systemctl restart breakfix-proxy.service
systemctl is-active --quiet breakfix-proxy.service
