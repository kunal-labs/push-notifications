#!/usr/bin/env bash
set -euo pipefail

echo "Running push-notifications delivery service in local environment..."
if [[ -f "main.go" ]]; then
  timeout -k 5 60s go run main.go
else
  echo "Service entrypoint main.go not present yet. Refer to documentation in docs/."
fi
