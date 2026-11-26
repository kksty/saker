#!/usr/bin/env sh
# Thin local-gate entry for Linux/macOS.
#
# All checks live in cmd/saker-gate (one implementation shared with scripts/gate.ps1),
# so Linux and Windows run exactly the same list. This script only locates the
# repository root, runs the gate and forwards its arguments and exit code.
#
#   ./scripts/gate.sh
#   ./scripts/gate.sh -skip-web -manifest-out .build/build-manifest.json
#   ./scripts/gate.sh -manifest-only -no-model-probe
#
# Run `./scripts/gate.sh -h` for every option.
set -eu

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)

command -v go >/dev/null 2>&1 || {
  echo "找不到 go：需要 Go 1.26+（Debian/Ubuntu: sudo apt-get install -y golang-go，或按 https://go.dev/dl/ 安装官方 tarball 并把 bin 加入 PATH）。" >&2
  exit 1
}

cd "$root"
exec go run ./cmd/saker-gate "$@"
