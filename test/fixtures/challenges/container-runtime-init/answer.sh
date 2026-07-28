#!/bin/bash
set -euo pipefail

mkdir -p /var/lib/breakfix/runtime-init-fixture
printf 'ready\n' >/var/lib/breakfix/runtime-init-fixture/ready
