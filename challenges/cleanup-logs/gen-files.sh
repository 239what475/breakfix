#!/bin/bash
# Creates test log files in /var/log for the cleanup-logs challenge
set -e

mkdir -p /var/log /backup

# Create old large files (>7 days, >100M)
for i in $(seq 1 3); do
    dd if=/dev/zero of=/var/log/old-large-$i.log bs=1M count=150 2>/dev/null
    touch -d "2020-01-01" /var/log/old-large-$i.log
done

# Create old small files (>7 days, <100M)
for i in $(seq 1 5); do
    dd if=/dev/zero of=/var/log/old-small-$i.log bs=1M count=10 2>/dev/null
    touch -d "2020-01-01" /var/log/old-small-$i.log
done

# Create new large files (<7 days, >100M)
for i in $(seq 1 2); do
    dd if=/dev/zero of=/var/log/new-large-$i.log bs=1M count=200 2>/dev/null
done

# Create new small files (<7 days, <100M)
for i in $(seq 1 5); do
    dd if=/dev/zero of=/var/log/app-$i.log bs=1M count=1 2>/dev/null
done

echo "Test log files created."
echo "Old (>7d) + Large (>100M): old-large-1.log old-large-2.log old-large-3.log"
echo "Old (>7d) + Small (<100M): old-small-1.log through old-small-5.log"
echo "New (<7d) + Large (>100M): new-large-1.log new-large-2.log"
echo "New (<7d) + Small (<100M): app-1.log through app-5.log"
