#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

echo "Building push-notifications sideplane-demo..."
mkdir -p bin
timeout -k 5 60s go build -o bin/sideplane-demo ./cmd/sideplane-demo
echo "Build complete: bin/sideplane-demo"
