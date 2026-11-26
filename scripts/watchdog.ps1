#!/usr/bin/env pwsh
# watchdog.ps1 — optional external caretaker for a long unattended run.
#
# It is NOT part of the engine. saker does not know it exists; it only reads
# project files and the console API, and (when explicitly told to) performs two
# narrow actions. Default mode is observe.
#
#   pwsh -File scripts/watchdog.ps1 -Project p-00000000                  # observe
#   pwsh -File scripts/watchdog.ps1 -Project p-1 -Mode resume             # + undo restart_pause
#   pwsh -File scripts/watchdog.ps1 -Project p-1 -Mode restart `
#        -StartCommand 'C:\saker\saker.exe --addr 127.0.0.1:18200 ...'
#
# Why it exists: an 8-hour unattended run needs something outside the process to
# notice that the process is gone. It is deliberately NOT a supervisor that
# kills and restarts a live control plane:
#
#   * resume only a pause whose cause is restart_pause (the boot path wrote it).
#     manual_pause / completion_standby / commander_standby / unrecorded are
#     decisions by a person or by the commander; undoing them is not this
#     script's job. real projects must not be resumed behind the operator.
#   * "start" only when the console port is free, i.e. the process is gone. A
#     live but silent control plane is logged for a human; force-killing saker can
#     lose an in-flight run's settlement, so the script does not do it.
#   * "stalled" requires both: no new event for -StallMinutes AND zero running
#     runs. If the running count cannot be read (no token, API down), the state
#     is "unverified" and nothing is done — unknown is not "stalled".
#
# It never writes inside the data directory. Its decisions go to a JSONL log
# outside it (-LogPath, default <repo>/_build-artifacts/watchdog/<project>.jsonl).
#
# Real pentest projects: do not run this. It is for the bench run, where the
# platform keeps supplying goals; the operator can always stop the script.
#
# Exit codes: 0 finished/one tick, 3 nothing to do because it refused (logged),
# 4 restart budget exhausted.
param(
    # Not mandatory: -SelfTest must be runnable on its own by whoever is verifying
    # this script, without inventing a project id first.
    [string]$Project = "",
    [string]$DataDir = "data",
    [string]$Addr = "127.0.0.1:18200",
    [string]$TokenFile = "",
    [int]$IntervalSeconds = 300,
    [int]$StallMinutes = 10,
    [ValidateSet("observe", "resume", "restart")][string]$Mode = "observe",
    [string]$StartCommand = "",
    [int]$MaxRestarts = 3,
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

# The decision table is a pure function so -SelfTest can prove every branch
# without a live instance, a clock, or a network.
function Get-Verdict {
    param(
        [datetime]$Now,
        $LastEvent,
        [bool]$Paused,
        [string]$PauseKind,
        $Running,          # int, or $null when unreadable
        [string]$Mode,
        [int]$Restarts,
        [int]$MaxRestarts,
        [bool]$PortListening,
        [bool]$HasStartCommand,
        [int]$StallMinutes
    )
    if ($Paused) {
        if ($PauseKind -eq "restart_pause") {
            if ($Mode -in @("resume", "restart")) {
                return [pscustomobject]@{ State = "paused_restart"; Action = "resume"; Reason = "平台重启造成的暂停，按 -Mode $Mode 恢复派发" }
            }
            return [pscustomobject]@{ State = "paused_restart"; Action = "none"; Reason = "平台重启造成的暂停；observe 模式只看不动" }
        }
        $label = if ($PauseKind) { $PauseKind } else { "未记录" }
        return [pscustomobject]@{ State = "paused_other"; Action = "none"; Reason = "暂停原因是 $label：不是重启造成的，不由看护恢复" }
    }
    if ($null -eq $LastEvent) {
        return [pscustomobject]@{ State = "unknown"; Action = "none"; Reason = "没有任何事件文件，无法判断是否停摆（不猜）" }
    }
    $idle = $Now - [datetime]$LastEvent
    if ($idle.TotalSeconds -lt 0) {
        return [pscustomobject]@{ State = "healthy"; Action = "none"; Reason = "最近事件时间在未来（时钟偏差），按新鲜处理" }
    }
    if ($idle.TotalMinutes -lt $StallMinutes) {
        if ($null -ne $Running -and $Running -gt 0) {
            return [pscustomobject]@{ State = "active"; Action = "none"; Reason = "有 $Running 个 run 在跑，且最近 $([int]$idle.TotalSeconds)s 内有事件" }
        }
        return [pscustomobject]@{ State = "healthy"; Action = "none"; Reason = "最近事件在 $([int]$idle.TotalSeconds)s 前" }
    }
    if ($null -eq $Running) {
        return [pscustomobject]@{ State = "unverified"; Action = "none"; Reason = "已 $([int]$idle.TotalMinutes) 分钟无事件，但读不到运行中的 run 数（token/API 不可用），不做动作" }
    }
    if ($Running -gt 0) {
        return [pscustomobject]@{ State = "active"; Action = "none"; Reason = "已静默 $([int]$idle.TotalMinutes) 分钟，但仍有 $Running 个 run 在跑：沉默不等于停摆" }
    }
    if ($Mode -eq "observe") {
        return [pscustomobject]@{ State = "stalled"; Action = "none"; Reason = "已 $([int]$idle.TotalMinutes) 分钟无事件且 0 个 run 在跑；observe 模式只记一条" }
    }
    if ($Mode -eq "resume") {
        return [pscustomobject]@{ State = "stalled"; Action = "none"; Reason = "停摆与暂停是两件事：resume 模式只处理 restart_pause" }
    }
    # Mode = restart: start the process only when it is gone.
    if ($Restarts -ge $MaxRestarts) {
        return [pscustomobject]@{ State = "stalled"; Action = "stop"; Reason = "停摆且已重启 $Restarts 次（上限 $MaxRestarts），停止并留证据给人工" }
    }
    if (-not $HasStartCommand) {
        return [pscustomobject]@{ State = "stalled"; Action = "none"; Reason = "停摆但未提供 -StartCommand：拒绝按猜测的命令启动" }
    }
    if ($PortListening) {
        return [pscustomobject]@{ State = "stalled"; Action = "none"; Reason = "停摆但平台还在监听：脚本不杀进程（杀进程可能丢结算），需人工" }
    }
    return [pscustomobject]@{ State = "stalled"; Action = "start"; Reason = "已 $([int]$idle.TotalMinutes) 分钟无事件、0 个 run、端口空出：按 -StartCommand 启动" }
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

function Test-Port([string]$addr) {
    $idx = $addr.LastIndexOf(":")
    if ($idx -lt 1) { return $false }
    $hostName = $addr.Substring(0, $idx).Trim("[", "]")
    $port = 0
    if (-not [int]::TryParse($addr.Substring($idx + 1), [ref]$port)) { return $false }
    $client = [System.Net.Sockets.TcpClient]::new()
    try {
        $task = $client.ConnectAsync($hostName, $port)
        return $task.Wait(1500) -and $client.Connected
    } catch {
        return $false
    } finally {
        $client.Dispose()
    }
}

function Invoke-Api([string]$method, [string]$url, [string]$token) {
    $headers = @{}
    if ($token) { $headers["Authorization"] = "Bearer $token" }
    try {
        $resp = Invoke-WebRequest -Method $method -Uri $url -Headers $headers -TimeoutSec 10 -UseBasicParsing -ErrorAction Stop
        return [pscustomobject]@{ ok = $true; status = [int]$resp.StatusCode; body = $resp.Content; error = $null }
    } catch {
        $status = $null
        try { if ($_.Exception.Response) { $status = [int]$_.Exception.Response.StatusCode } } catch { }
        return [pscustomobject]@{ ok = $false; status = $status; body = $null; error = $_.Exception.Message }
    }
}

function Get-RunningCount([string]$base, [string]$project, [string]$token) {
    $r = Invoke-Api "GET" "$base/p/$project/api/runs" $token
    if (-not $r.ok) { return $null }
    try {
        $j = $r.body | ConvertFrom-Json
        return @($j.runs | Where-Object { $_.status -eq "running" }).Count
    } catch {
        return $null
    }
}

function Write-Log([string]$path, $record) {
    New-Item -ItemType Directory -Force -Path (Split-Path -Parent $path) | Out-Null
    ($record | ConvertTo-Json -Compress -Depth 6) | Add-Content -LiteralPath $path -Encoding utf8
}

function Invoke-Tick {
    param([string]$projectDir, [string]$token, [string]$base, [string]$mode, [string]$startCommand,
        [int]$stallMinutes, [int]$maxRestarts, [int]$restarts)
    $p = Get-Content -LiteralPath (Join-Path $projectDir "project.json") -Raw | ConvertFrom-Json
    $lastEvent = Get-NewestEvent $projectDir
    $running = Get-RunningCount $base $Project $token
    $listening = Test-Port $Addr
    $verdict = Get-Verdict -Now ([datetime]::UtcNow) -LastEvent $lastEvent -Paused ([bool]$p.paused) `
        -PauseKind ([string]$p.pause_kind) -Running $running -Mode $mode -Restarts $restarts `
        -MaxRestarts $maxRestarts -PortListening $listening -HasStartCommand ([bool]$startCommand) `
        -StallMinutes $stallMinutes
    $record = [ordered]@{
        at             = (Get-Date).ToUniversalTime().ToString("yyyy-MM-ddTHH:mm:ssZ")
        project        = $Project
        mode           = $mode
        state          = $verdict.State
        action         = $verdict.Action
        reason         = $verdict.Reason
        paused         = [bool]$p.paused
        pause_kind     = [string]$p.pause_kind
        last_event_utc = if ($lastEvent) { $lastEvent.ToString("yyyy-MM-ddTHH:mm:ssZ") } else { $null }
        running        = $running
        port_listening = $listening
        restarts       = $restarts
    }
    return $record
}

function Invoke-SelfTest {
    $failures = [System.Collections.Generic.List[string]]::new()
    function Check([string]$name, $got, [string]$wantState, [string]$wantAction) {
        if ($got.State -ne $wantState -or $got.Action -ne $wantAction) {
            $failures.Add("$name：得到 $($got.State)/$($got.Action)，期望 $wantState/$wantAction（$($got.Reason)）")
        }
    }
    $now = [datetime]::UtcNow
    $stale = $now.AddMinutes(-11)
    $fresh = $now.AddMinutes(-1)
    # V fills the defaults and lets one case override only what it cares about;
    # passing every parameter explicitly keeps a duplicated-name error from
    # hiding a branch that never ran.
    function V([hashtable]$overrides) {
        $p = @{
            Now = $now; LastEvent = $null; Paused = $false; PauseKind = ""; Running = 0
            Mode = "observe"; Restarts = 0; MaxRestarts = 3; PortListening = $false
            HasStartCommand = $false; StallMinutes = 10
        }
        foreach ($k in $overrides.Keys) { $p[$k] = $overrides[$k] }
        Get-Verdict @p
    }
    Check "新鲜事件" (V @{ LastEvent = $fresh }) "healthy" "none"
    Check "静默但有 run 在跑" (V @{ LastEvent = $stale; Running = 1 }) "active" "none"
    Check "静默且运行数不可知" (V @{ LastEvent = $stale; Running = $null }) "unverified" "none"
    Check "静默且 0 run（observe）" (V @{ LastEvent = $stale }) "stalled" "none"
    Check "静默且 0 run（resume）" (V @{ LastEvent = $stale; Mode = "resume" }) "stalled" "none"
    Check "重启模式：平台还在跑则不杀" (V @{ LastEvent = $stale; Mode = "restart"; PortListening = $true; HasStartCommand = $true }) "stalled" "none"
    Check "重启模式：没有启动命令则不动" (V @{ LastEvent = $stale; Mode = "restart" }) "stalled" "none"
    Check "重启模式：端口空出且给了命令" (V @{ LastEvent = $stale; Mode = "restart"; HasStartCommand = $true }) "stalled" "start"
    Check "重启预算用尽" (V @{ LastEvent = $stale; Mode = "restart"; HasStartCommand = $true; Restarts = 3 }) "stalled" "stop"
    Check "restart_pause + resume" (V @{ Paused = $true; PauseKind = "restart_pause"; Mode = "resume" }) "paused_restart" "resume"
    Check "restart_pause + observe 不动" (V @{ Paused = $true; PauseKind = "restart_pause" }) "paused_restart" "none"
    Check "manual_pause 不恢复" (V @{ Paused = $true; PauseKind = "manual_pause"; Mode = "restart"; HasStartCommand = $true }) "paused_other" "none"
    Check "completion_standby 不恢复" (V @{ Paused = $true; PauseKind = "completion_standby"; Mode = "resume" }) "paused_other" "none"
    Check "无 pause_kind 不恢复" (V @{ Paused = $true; PauseKind = ""; Mode = "resume" }) "paused_other" "none"
    Check "没有任何事件文件" (V @{}) "unknown" "none"
    Check "事件时间在未来" (V @{ LastEvent = $now.AddMinutes(5) }) "healthy" "none"
    if ($failures.Count -gt 0) {
        Write-Host "SELFTEST 失败 $($failures.Count) 项：" -ForegroundColor Red
        foreach ($f in $failures) { Write-Host "  - $f" }
        exit 1
    }
    Write-Host "SELFTEST OK 16/16" -ForegroundColor Green
    exit 0
}

if ($SelfTest) { Invoke-SelfTest }

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
if ($Mode -ne "observe" -and -not $TokenFile) {
    $TokenFile = Join-Path $dataAbs "console.token"
}
if ($Mode -ne "observe" -and -not (Test-Path -LiteralPath $TokenFile)) {
    [Console]::Error.WriteLine("找不到控制台令牌：$TokenFile（看护要读 run 状态并用 API 恢复，必须能认证）")
    exit 1
}
$token = ""
if ($TokenFile -and (Test-Path -LiteralPath $TokenFile)) { $token = (Get-Content -LiteralPath $TokenFile -Raw).Trim() }
$base = "http://$Addr"
if (-not $LogPath) {
    $LogPath = Join-Path $root "_build-artifacts/watchdog/$Project.jsonl"
}
$logAbs = if ($LogPath -eq "-") { "" } else { Resolve-FromRoot $LogPath }

Write-Host "watchdog：项目 $Project，模式 $Mode，间隔 ${IntervalSeconds}s，停摆阈值 ${StallMinutes}m，日志 $(if ($logAbs) { $logAbs } else { 'stdout' })"
Write-Host "口径：只在重启造成的暂停上 resume；只在进程消失（端口空）时 start；未知一律不动。"

$restarts = 0
while ($true) {
    try {
        $record = Invoke-Tick -projectDir $projectDir -token $token -base $base -mode $Mode `
            -startCommand $StartCommand -stallMinutes $StallMinutes -maxRestarts $MaxRestarts -restarts $restarts
        switch ($record.action) {
            "resume" {
                $r = Invoke-Api "POST" "$base/p/$Project/api/project/resume" $token
                $record["action_result"] = if ($r.ok) { "resumed" } else { "resume_failed: $($r.error)" }
            }
            "start" {
                try {
                    Start-Process -FilePath "pwsh" -ArgumentList @("-NoProfile", "-Command", $StartCommand) -WindowStyle Hidden | Out-Null
                    $record["action_result"] = "started"
                    $restarts++
                } catch {
                    $record["action_result"] = "start_failed: $($_.Exception.Message)"
                }
            }
            "stop" {
                if ($logAbs) { Write-Log $logAbs $record } else { $record | ConvertTo-Json -Compress }
                Write-Host "看护停止：$($record.reason)" -ForegroundColor Yellow
                exit 4
            }
            default { }
        }
        if ($logAbs) { Write-Log $logAbs $record } else { $record | ConvertTo-Json -Compress }
        Write-Host ("[{0}] {1}/{2}：{3}" -f $record.at, $record.state, $record.action, $record.reason)
    } catch {
        Write-Host "看护本轮失败：$($_.Exception.Message)" -ForegroundColor Yellow
    }
    if ($Once) { exit 0 }
    Start-Sleep -Seconds $IntervalSeconds
}
