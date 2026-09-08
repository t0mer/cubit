#!/usr/bin/env bash
# Run Cubit locally against a writable data directory.
#
# Credentials come from the environment; nothing here is committed:
#   export CUBIT_PLUXEE_USERNAME=... CUBIT_PLUXEE_PASSWORD=...
#   export CUBIT_AUTH_ENCRYPTION_KEY="$(head -c 32 /dev/urandom | base64)"
set -euo pipefail
cd "$(dirname "$0")/.."

: "${CUBIT_PLUXEE_USERNAME:?set CUBIT_PLUXEE_USERNAME}"
: "${CUBIT_PLUXEE_PASSWORD:?set CUBIT_PLUXEE_PASSWORD}"
: "${CUBIT_AUTH_ENCRYPTION_KEY:?set CUBIT_AUTH_ENCRYPTION_KEY (32 bytes, raw/hex/base64)}"

export CUBIT_DATA_DIR="${CUBIT_DATA_DIR:-$PWD/data}"
export CUBIT_LOG_FORMAT="${CUBIT_LOG_FORMAT:-text}"
export CUBIT_LOG_LEVEL="${CUBIT_LOG_LEVEL:-debug}"
mkdir -p "$CUBIT_DATA_DIR"

go run -ldflags "-X github.com/t0mer/cubit/internal/version.Version=dev" \
  ./cmd/cubit --config "${CUBIT_CONFIG:-./config/config.yaml}" "$@"
