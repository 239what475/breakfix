#!/bin/bash
# Creates test log files in /var/log for the cleanup-logs challenge.
set -e

mkdir -p /var/log /backup /var/lib/breakfix/cleanup-logs

# Old large files: should be archived.
for i in $(seq 1 3); do
    dd if=/dev/zero of=/var/log/old-large-$i.log bs=1M count=150 2>/dev/null
    touch -d "2020-01-01" /var/log/old-large-$i.log
done

# Old small files: should stay in place.
for i in $(seq 1 5); do
    dd if=/dev/zero of=/var/log/old-small-$i.log bs=1M count=10 2>/dev/null
    touch -d "2020-01-01" /var/log/old-small-$i.log
done

# The checker uses this fixture manifest instead of rediscovering files. This
# keeps the intended outcomes stable even when a valid cleanup removes source
# logs after archiving them.
cat >/var/lib/breakfix/cleanup-logs/eligible.txt <<'EOF'
old-large-1.log
old-large-2.log
old-large-3.log
EOF

cat >/var/lib/breakfix/cleanup-logs/protected.txt <<'EOF'
old-small-1.log
old-small-2.log
old-small-3.log
old-small-4.log
old-small-5.log
new-large-1.log
new-large-2.log
app-1.log
app-2.log
app-3.log
app-4.log
app-5.log
EOF

# New large files: should stay in place.
for i in $(seq 1 2); do
    dd if=/dev/zero of=/var/log/new-large-$i.log bs=1M count=200 2>/dev/null
done

# New small files: should stay in place.
for i in $(seq 1 5); do
    dd if=/dev/zero of=/var/log/app-$i.log bs=1M count=1 2>/dev/null
done
