#!/bin/bash
set -e

SCRIPT="/usr/local/bin/cleanup.sh"

# 1. Script must exist and be executable
if [ ! -x "$SCRIPT" ]; then
    echo "FAIL: $SCRIPT not found or not executable"
    exit 1
fi

# 2. Run the user's script with timeout
timeout 60 bash "$SCRIPT" || {
    echo "FAIL: script exited with error or timed out"
    exit 1
}

# 3. /backup directory must exist (should be created by script if needed)
if [ ! -d /backup ]; then
    echo "FAIL: /backup directory not found"
    exit 1
fi

# 4. Check that old-large files have corresponding archives
echo "Checking old large files are archived..."
PASS=0
find /var/log -type f -name "*.log" -mtime +6 -size +100M | while IFS= read -r f; do
    name=$(basename "$f")
    if ls "/backup/${name%.log}"*.tar.gz >/dev/null 2>&1; then
        echo "  OK: $name archived"
    else
        echo "  MISSING: $name not archived in /backup"
        exit 1
    fi
done
PASS=$?
if [ $PASS -ne 0 ]; then
    exit 1
fi

# 5. Check that wrong files are NOT archived
#    New files and small files should stay in /var/log
echo "Checking new/small files are NOT archived..."
find /var/log -type f -name "*.log" \( -mtime -7 -o -size -100M \) | while IFS= read -r f; do
    name=$(basename "$f")
    if ls "/backup/${name%.log}"*.tar.gz >/dev/null 2>&1; then
        echo "  WRONG: $name should not be archived (too new or too small)"
        exit 1
    fi
done
echo "  OK: all correct"

# 6. Verify archives are valid tar.gz
echo "Checking archive validity..."
find /backup -name "*.tar.gz" | while IFS= read -r a; do
    if tar -tzf "$a" >/dev/null 2>&1; then
        echo "  OK: $(basename "$a") is valid"
    else
        echo "  CORRUPT: $(basename "$a") is not a valid tar.gz"
        exit 1
    fi
done

echo ""
echo "✓ All checks passed!"
exit 0
