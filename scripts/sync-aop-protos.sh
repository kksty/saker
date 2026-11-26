#!/usr/bin/env sh
# Thin entry: sync the vendored AOP generated code from the cyber-harness source.
#
# All copy/rewrite/check logic lives in cmd/saker-gate (-sync-aop-protos), the same
# single implementation scripts/gate.ps1 and scripts/gate.sh run; this script
# only locates the repository root, runs that mode and forwards arguments and
# exit code.
#
#   ./scripts/sync-aop-protos.sh
#   ./scripts/sync-aop-protos.sh -aop-dry-run
#   ./scripts/sync-aop-protos.sh -harness-dir ../cyber-harness
#
# Only generated files are copied; saker never edits the wire semantics. The
# harness source is resolved like the gate: -harness-dir, $HARNESS_DIR,
# .build/harness-src, then a sibling ../cyber-harness. The command runs the same
# drift check the gate runs after copying, so a sync that still differs fails.
#
# Run `./scripts/gate.sh -h` for the shared options.
set -eu

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)

command -v go >/dev/null 2>&1 || {
  echo "找不到 go：需要 Go 1.26+（Debian/Ubuntu: sudo apt-get install -y golang-go，或按 https://go.dev/dl/ 安装官方 tarball 并把 bin 加入 PATH）。" >&2
  exit 1
}

cd "$root"
exec go run ./cmd/saker-gate -sync-aop-protos "$@"
