#!/usr/bin/env pwsh
# Thin local-gate entry for Windows.
#
# All checks live in cmd/saker-gate (one implementation shared with scripts/gate.sh),
# so Windows and Linux run exactly the same list. This script only locates the
# repository root, runs the gate and forwards its arguments and exit code.
#
#   pwsh -File scripts/gate.ps1
#   pwsh -File scripts/gate.ps1 -skip-web -manifest-out .build/build-manifest.json
#   pwsh -File scripts/gate.ps1 -manifest-only -no-model-probe
#
# Run `pwsh -File scripts/gate.ps1 -h` for every option.
$ErrorActionPreference = "Stop"

$root = Split-Path -Parent $PSScriptRoot
if (-not (Get-Command go -ErrorAction SilentlyContinue)) {
    [Console]::Error.WriteLine("找不到 go：需要 Go 1.26+（winget install GoLang.Go 或 https://go.dev/dl/），装好后重开终端。")
    exit 1
}

Push-Location $root
try {
    # The repository's own gate runs serial (`go test ./cmd/... ./internal/...
    # -p 1`), but this host also runs a live project: parallel package tests
    # fight it for loopback connects, and 2026-11-22 that surfaced as connect
    # timeouts reported as test failures (aopexec 1, api 2 — each passing
    # alone). The gate scopes Go steps to ./cmd/... ./internal/... because the
    # default data directory is inside the module.
    # Default the forwarded flags to serial unless the caller chose test flags
    # explicitly. saker-gate replaces its own default when -test-flags is given, so
    # the 20m timeout that default carried is repeated here.
    # gate.sh is deliberately untouched: it is a separate entry point and must
    # keep behaving exactly as before.
    $forward = @($args)
    $hasTestFlags = $false
    $wantsHelp = $false
    foreach ($a in $forward) {
        if ($a -match '^-{1,2}test-flags(=|$)') { $hasTestFlags = $true }
        if ($a -match '^-{1,2}(h|help)$') { $wantsHelp = $true }
    }
    if (-not $hasTestFlags -and -not $wantsHelp) {
        $forward += @('-test-flags', '-p 1 -timeout 20m')
    }
    & go run ./cmd/saker-gate @forward
    exit $LASTEXITCODE
}
finally {
    Pop-Location
}
