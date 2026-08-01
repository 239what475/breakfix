#!/bin/bash
set -euo pipefail

mkdir -p /usr/local/bin

cat >/usr/local/bin/cleanup.sh <<'EOF'
#!/bin/bash
set -euo pipefail

mkdir -p /backup

find /var/log -type f -name "*.log" -mtime +6 -size +100M -print0 |
while IFS= read -r -d '' file; do
    name="$(basename "$file" .log)"
    tar -czf "/backup/${name}.tar.gz" -C "$(dirname "$file")" "$(basename "$file")"
done
EOF

chmod +x /usr/local/bin/cleanup.sh
/usr/local/bin/cleanup.sh
