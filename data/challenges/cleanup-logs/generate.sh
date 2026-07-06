#!/bin/bash
# Creates test log files in /var/log for the cleanup-logs challenge.
set -e

mkdir -p /var/log /backup

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

# New large files: should stay in place.
for i in $(seq 1 2); do
    dd if=/dev/zero of=/var/log/new-large-$i.log bs=1M count=200 2>/dev/null
done

# New small files: should stay in place.
for i in $(seq 1 5); do
    dd if=/dev/zero of=/var/log/app-$i.log bs=1M count=1 2>/dev/null
done
