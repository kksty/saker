#!/usr/bin/env bash
# Run Saker from source on Linux/macOS (no build step required).
#   ./run.sh
#   HARNESS=/opt/cyber/cyber-harness SCAN=/opt/cyber/cyber-scan ./run.sh
#   PROJECT=acme-web ./run.sh
set -euo pipefail

ADDR="${ADDR:-127.0.0.1:8080}"
DATA="${DATA:-./data}"
PROJECT="${PROJECT:-}"
HARNESS="${HARNESS:-}"
SCAN="${SCAN:-}"

if ! command -v go >/dev/null 2>&1; then
  echo "go not found. Install Go 1.26+ first." >&2
  exit 1
fi

args=(run ./cmd/saker --addr "$ADDR" --data "$DATA")
if [ -n "$PROJECT" ]; then
  args+=(--project "$PROJECT")
fi
if [ -n "$HARNESS" ]; then args+=(--harness "$HARNESS"); fi
if [ -n "$SCAN" ]; then args+=(--scan "$SCAN"); fi

echo "Saker -> http://$ADDR"
exec go "${args[@]}"
