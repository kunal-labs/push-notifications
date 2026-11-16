#!/usr/bin/env bash
set -euo pipefail

cat << 'EOF'
push-notifications build & verification targets:

  make build        Compile bin/sideplane-demo binary
  make test         Run tidiness, diagram/doc hygiene, and Go race test suite
  make run          Run CLI side-plane benchmark (--mode=cli)
  make clean        Clean build artifacts, temporary files, and Go caches
  make tidy-check   Verify repository tidiness, script hygiene, and README sync
  make help         Display this target overview

Cloud Run sandbox deployment:
  ./scripts/deploy-cloudrun.sh   Deploy containerized server (--mode=server) to kunal-scratch
EOF
