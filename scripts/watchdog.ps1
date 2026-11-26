# Read-only project observer. The deployment's process supervisor owns process
# restarts. Saker owns project reconciliation and resume.
param(
    [string]$Project = "",
    [string]$DataDir = "data",
    [string]$Addr = "127.0.0.1:18200",
    [string]$TokenFile = "",
    [int]$IntervalSeconds = 300,
    [int]$StallMinutes = 10,
    [string]$LogPath = "",
    [switch]$Once,
    [switch]$SelfTest
)

$ErrorActionPreference = "Stop"
try { [Console]::OutputEncoding = [System.Text.UTF8Encoding]::new($false) } catch { }

$root = Split-Path -Parent $PSScriptRoot

function Resolve-FromRoot([string]$path) {
    if ([System.IO.Path]::IsPathRooted($path)) { return [System.IO.Path]::GetFullPath($path) }
    return [System.IO.Path]::GetFullPath((Join-Path $root $path))
}

function Get-Verdict([datetime]$now, $lastEvent, [bool]$paused, [string]$pauseKind, $running, [int]$stallMinutes) {
    if ($null -eq $running) {
        return @{ State = "unverified"; Reason = "API 未返回有效运行列表，无法核实项目状态" }
    }
    if ($paused) {
        return @{ State = "paused"; Reason = "项目已暂停，原因：$pauseKind" }
    }
    if ($running -gt 0) {
        return @{ State = "active"; Reason = "运行记录中有 $running 个在途运行" }
    }
    if ($null -eq $lastEvent) {
        return @{ State = "unknown"; Reason = "没有事件时间记录" }
    }
    $idle = $now - [datetime]$lastEvent
    if ($idle.TotalMinutes -ge $stallMinutes) {
        return @{ State = "stalled"; Reason = "已 $([int]$idle.TotalMinutes) 分钟无事件，运行记录中没有在途运行" }
    }
    return @{ State = "recent"; Reason = "最近事件在 $([Math]::Max(0, [int]$idle.TotalSeconds)) 秒前" }
}

function Get-NewestEvent([string]$projectDir) {
    $paths = @()
    foreach ($name in @("ledger.sqlite", "ledger.sqlite-wal", "decisions.jsonl", "project.json", "state.md")) {
        $p = Join-Path $projectDir $name
        if (Test-Path -LiteralPath $p) { $paths += Get-Item -LiteralPath $p }
    }
    $runsDir = Join-Path $projectDir "runs"
    if (Test-Path -LiteralPath $runsDir) {
        $newest = Get-ChildItem -LiteralPath $runsDir -Filter "*.jsonl" -File -ErrorAction SilentlyContinue |
            Sort-Object LastWriteTimeUtc -Descending | Select-Object -First 1
        if ($newest) { $paths += $newest }
    }
    if ($paths.Count -eq 0) { return $null }
    return ($paths | Sort-Object LastWriteTimeUtc -Descending | Select-Object -First 1).LastWriteTimeUtc
}

function Get-RunningCount([string]$base, [string]$project, [string]$token) {
    $headers = @{}
    if ($token) { $headers["Authorization"] = "Bearer $token" }
    try {
        $result = Invoke-RestMethod -Uri "$base/p/$([Uri]::EscapeDataString($project))/api/runs" -Headers $headers -TimeoutSec 10
        if ($null -eq $result -or -not $result.PSObject.Properties["runs"]) { return $null }
        return @($result.runs | Where-Object { $_.status -eq "running" }).Count
    } catch {
        return $null
    }
}

function Write-Log([string]$path, $record) {
    New-Item -ItemType Directory -Force -Path (Split-Path -Parent $path) | Out-Null
    ($record | ConvertTo-Json -Compress -Depth 6) | Add-Content -LiteralPath $path -Encoding utf8
}

function Invoke-Tick {
    $p = Get-Content -LiteralPath (Join-Path $projectDir "project.json") -Raw | ConvertFrom-Json
    $lastEvent = Get-NewestEvent $projectDir
    $running = Get-RunningCount $base $Project $token
    $now = [datetime]::UtcNow
    $verdict = Get-Verdict $now $lastEvent ([bool]$p.paused) ([string]$p.pause_kind) $running $StallMinutes
    [ordered]@{
        at             = $now.ToString("o")
        project        = $Project
        state          = $verdict.State
        reason         = $verdict.Reason
        manifest_paused = [bool]$p.paused
        manifest_pause_kind = [string]$p.pause_kind
        last_event_utc = if ($lastEvent) { $lastEvent.ToString("o") } else { $null }
        running        = $running
    }
}

if ($SelfTest) {
    $now = [datetime]::UtcNow
    $stale = $now.AddMinutes(-20)
    $fresh = $now.AddMinutes(-1)
    # Even a fresh manifest cannot establish API availability.
    if ((Get-Verdict $now $fresh $true "restart_pause" $null 10).State -ne "unverified") { throw "API 不可达必须记录为未核实" }
    if ((Get-Verdict $now $stale $false "" 1 10).State -ne "active") { throw "静默时保留在途记录" }
    if ((Get-Verdict $now $stale $false "" 0 10).State -ne "stalled") { throw "静默且无在途运行时记录停摆" }
    if ((Get-Verdict $now $fresh $true "manual_pause" 0 10).State -ne "paused") { throw "保留暂停状态" }
    if ((Get-Verdict $now $null $false "" 0 10).State -ne "unknown") { throw "缺少事件时状态未知" }
    Write-Host "SELFTEST OK 5/5"
    exit 0
}

if (-not $Project) {
    [Console]::Error.WriteLine("缺少 -Project：看护一次只盯一个项目（-SelfTest 除外）")
    exit 2
}

$dataAbs = Resolve-FromRoot $DataDir
$projectDir = Join-Path $dataAbs ("projects/" + $Project)
if (-not (Test-Path -LiteralPath (Join-Path $projectDir "project.json"))) {
    [Console]::Error.WriteLine("找不到项目：$projectDir（用 -Project 与 -DataDir 指向实例）")
    exit 1
}
if (-not $TokenFile) { $TokenFile = Join-Path $dataAbs "console.token" }
$token = ""
if (Test-Path -LiteralPath $TokenFile) { $token = (Get-Content -LiteralPath $TokenFile -Raw).Trim() }
$base = "http://$Addr"
if (-not $LogPath) {
    $LogPath = Join-Path $root "_build-artifacts/watchdog/$Project.jsonl"
}
$logAbs = if ($LogPath -eq "-") { "" } else { Resolve-FromRoot $LogPath }

Write-Host "watchdog：观察项目 $Project，间隔 ${IntervalSeconds}s，停摆阈值 ${StallMinutes}m"
while ($true) {
    try {
        $record = Invoke-Tick
        if ($logAbs) { Write-Log $logAbs $record } else { $record | ConvertTo-Json -Compress }
        Write-Host ("[{0}] {1}：{2}" -f $record.at, $record.state, $record.reason)
    } catch {
        Write-Warning "看护本轮失败：$($_.Exception.Message)"
        if ($Once) { exit 1 }
    }
    if ($Once) { exit 0 }
    Start-Sleep -Seconds $IntervalSeconds
}
