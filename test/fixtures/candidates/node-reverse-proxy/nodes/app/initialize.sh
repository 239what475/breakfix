#!/bin/bash
set -euo pipefail

cat >/usr/local/bin/breakfix-demo-response <<'EOF'
#!/bin/bash
set -euo pipefail

# Wait for the request header so socat keeps both streams connected until the
# response is ready to be forwarded.
while IFS= read -r line; do
  [[ "$line" == $'\r' ]] && break
done
printf 'HTTP/1.1 200 OK\r\nContent-Length: 19\r\nConnection: close\r\n\r\nbreakfix-multi-node'
EOF
chmod 0755 /usr/local/bin/breakfix-demo-response

cat >/etc/systemd/system/breakfix-app.service <<'EOF'
[Unit]
Description=Breakfix fixture application
After=network-online.target

[Service]
ExecStart=/usr/bin/socat TCP-LISTEN:8080,reuseaddr,fork EXEC:/usr/local/bin/breakfix-demo-response
Restart=on-failure

[Install]
WantedBy=multi-user.target
EOF
systemctl daemon-reload
systemctl enable --now breakfix-app.service
