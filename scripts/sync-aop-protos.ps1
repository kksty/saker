#!/usr/bin/env pwsh
# Thin entry: sync the vendored AOP generated code from the cyber-harness source.
#
# All copy/rewrite/check logic lives in cmd/saker-gate (-sync-aop-protos), the same
# single implementation scripts/gate.ps1 and scripts/gate.sh run; this script
# only locates the repository root, runs that mode and forwards arguments and
# exit code.
#
#   pwsh -File scripts/sync-aop-protos.ps1
#   pwsh -File scripts/sync-aop-protos.ps1 -aop-dry-run
#   pwsh -File scripts/sync-aop-protos.ps1 -harness-dir ../cyber-harness
#
# Only generated files are copied; saker never edits the wire semantics. The
# harness source is resolved like the gate: -harness-dir, $HARNESS_DIR,
# .build/harness-src, then a sibling ../cyber-harness. The command runs the same
# drift check the gate runs after copying, so a sync that still differs fails.
#
# Run `pwsh -File scripts/gate.ps1 -h` for the shared options.
$ErrorActionPreference = "Stop"

$root = Split-Path -Parent $PSScriptRoot
if (-not (Get-Command go -ErrorAction SilentlyContinue)) {
    [Console]::Error.WriteLine("找不到 go：需要 Go 1.26+（winget install GoLang.Go 或 https://go.dev/dl/），装好后重开终端。")
    exit 1
}

Push-Location $root
try {
    & go run ./cmd/saker-gate -sync-aop-protos @args
    exit $LASTEXITCODE
}
finally {
    Pop-Location
}
