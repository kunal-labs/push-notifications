#!/usr/bin/env bash
set -euo pipefail

cat << 'EOF'
push-notifications build & verification targets:

  make build        Build platform binaries and verify dependencies
  make test         Execute verification and test suite
  make run          Launch platform service in local development mode
  make clean        Clean build artifacts, temporary files, and caches
  make tidy-check   Verify repository tidiness and structure standards
  make help         Display this target overview
EOF
