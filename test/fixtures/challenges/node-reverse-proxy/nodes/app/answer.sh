#!/bin/bash
set -euo pipefail

systemctl is-active --quiet breakfix-app.service
