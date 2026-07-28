#!/bin/bash
set -euo pipefail

mkdir -p /var/lib/breakfix/dependency-fixture
printf 'enabled=true\n' >/var/lib/breakfix/dependency-fixture/config
printf 'service=enabled\n' >/var/lib/breakfix/dependency-fixture/state
