#!/usr/bin/env pwsh
# collect-run.ps1 — package one run for review. Read-only: it copies, never
# opens the ledger for writing and never changes project files.
#
#   pwsh -File scripts/collect-run.ps1
#   pwsh -File scripts/collect-run.ps1 -Project p-00000000 -Out D:\handoff\run.zip
#   pwsh -File scripts/collect-run.ps1 -SubmitLog data/projects/p-1/submissions.jsonl
#
# What goes into the zip (fixed list — a missing item is written into MISSING.txt
# with the reason instead of being skipped silently):
#
#   ledger.sqlite (+ -wal / -shm when present)
#   runs/*.jsonl            every run journal
#   decisions.jsonl
#   project.json
#   state.md
#   operator-actions.jsonl
#   submit/<name>.jsonl     the submitter log (see -SubmitLog)
#   manifest/build-manifest.json
#   COLLECT.json            what was collected, with sizes/sha256 and the source
#
# The zip is written outside the project directory; the project directory is
# only read. -SelfTest proves that on a synthetic tree: it builds a fixture,
# collects it twice (with and without the optional files) and checks that the
# source bytes did not change.
#
# Exit codes: 0 collected (missing items are documented), 1 hard failure,
# 3 -FailOnMissing and something was missing.
param(
    [string]$DataDir = "data",
    [string]$Project = "",
    [string]$Out = "",
    [string[]]$SubmitLog = @(),
    [string]$Manifest = ".build/build-manifest.json",
    [switch]$FailOnMissing,
    [switch]$SelfTest
)

$ErrorActionPreference = "Stop"
try {
    [Console]::OutputEncoding = [System.Text.UTF8Encoding]::new($false)
} catch {
    # A redirected host may refuse; the zip contents are written as UTF-8 anyway.
}

$root = Split-Path -Parent $PSScriptRoot

function Resolve-FromRoot([string]$path) {
    if ([System.IO.Path]::IsPathRooted($path)) { return [System.IO.Path]::GetFullPath($path) }
    return [System.IO.Path]::GetFullPath((Join-Path $root $path))
}

function Get-Sha256([string]$path) {
    return (Get-FileHash -LiteralPath $path -Algorithm SHA256).Hash.ToLowerInvariant()
}

# Copy-StableFile retries when the source changes under it. A running control
# plane writes the ledger and the journals while this script reads them; a torn
# copy that looks complete is worse than a recorded retry.
function Copy-StableFile([string]$source, [string]$dest) {
    New-Item -ItemType Directory -Force -Path (Split-Path -Parent $dest) | Out-Null
    $note = $null
    for ($i = 1; $i -le 3; $i++) {
        $before = Get-Item -LiteralPath $source
        Copy-Item -LiteralPath $source -Destination $dest -Force
        $after = Get-Item -LiteralPath $source
        if ($before.Length -eq $after.Length -and $before.LastWriteTimeUtc -eq $after.LastWriteTimeUtc) {
            return $note
        }
        $note = "复制期间源文件在变化（第 $i 次尝试），已重试"
    }
    return "复制期间源文件持续变化，快照可能不一致（已如实记录）"
}

function New-CollectRequest([string]$dataAbs, [string]$projectDir, [string[]]$submitLogs, [string]$manifestAbs) {
    $projectId = Split-Path -Leaf $projectDir
    $items = [System.Collections.Generic.List[object]]::new()
    $missing = [System.Collections.Generic.List[object]]::new()

    function Add-Item([string]$destRel, [string]$source, [string]$label) {
        $items.Add([pscustomobject]@{ dest = $destRel; source = $source; label = $label })
    }

    # ledger + sidecars
    $ledger = Join-Path $projectDir "ledger.sqlite"
    if (Test-Path -LiteralPath $ledger) {
        Add-Item "ledger.sqlite" $ledger "账本"
        foreach ($suffix in @("-wal", "-shm")) {
            $side = "$ledger$suffix"
            if (Test-Path -LiteralPath $side) { Add-Item "ledger.sqlite$suffix" $side "账本边车" }
        }
    } else {
        $missing.Add([pscustomobject]@{ path = "ledger.sqlite"; reason = "文件不存在：$ledger" })
    }

    # run journals
    $runsDir = Join-Path $projectDir "runs"
    if (Test-Path -LiteralPath $runsDir) {
        $journals = @(Get-ChildItem -LiteralPath $runsDir -Filter "*.jsonl" -File | Sort-Object Name)
        if ($journals.Count -eq 0) {
            $missing.Add([pscustomobject]@{ path = "runs/*.jsonl"; reason = "目录存在但没有任何 run journal" })
        }
        foreach ($j in $journals) { Add-Item ("runs/" + $j.Name) $j.FullName "run journal" }
    } else {
        $missing.Add([pscustomobject]@{ path = "runs/"; reason = "目录不存在：$runsDir" })
    }

    foreach ($name in @("decisions.jsonl", "project.json", "state.md", "operator-actions.jsonl")) {
        $path = Join-Path $projectDir $name
        if (Test-Path -LiteralPath $path) { Add-Item $name $path $name } else {
            $missing.Add([pscustomobject]@{ path = $name; reason = "文件不存在：$path" })
        }
    }

    # submitter log: explicit paths win; otherwise discover *submit*.jsonl
    $resolvedSubmit = @()
    foreach ($candidate in $submitLogs) {
        $abs = Resolve-FromRoot $candidate
        if (Test-Path -LiteralPath $abs) { $resolvedSubmit += $abs } else {
            $missing.Add([pscustomobject]@{ path = "submit/" + (Split-Path -Leaf $abs); reason = "-SubmitLog 指定的文件不存在：$abs" })
        }
    }
    if ($resolvedSubmit.Count -eq 0 -and $submitLogs.Count -eq 0) {
        $found = @()
        foreach ($dir in @($projectDir, $dataAbs)) {
            if (-not (Test-Path -LiteralPath $dir)) { continue }
            $found += @(Get-ChildItem -LiteralPath $dir -Filter "*submit*.jsonl" -File -Recurse -ErrorAction SilentlyContinue |
                Select-Object -First 20)
        }
        if ($found.Count -gt 0) {
            $resolvedSubmit += @($found | ForEach-Object { $_.FullName } | Sort-Object -Unique)
        } else {
            $missing.Add([pscustomobject]@{
                    path   = "submit/*.jsonl"
                    reason = "没有提交器日志：未指定 -SubmitLog，项目目录与数据目录下也没有 *submit*.jsonl（提交器可能尚未启用）"
                })
        }
    }
    foreach ($path in $resolvedSubmit) { Add-Item ("submit/" + (Split-Path -Leaf $path)) $path "提交器日志" }

    if (Test-Path -LiteralPath $manifestAbs) {
        Add-Item "manifest/build-manifest.json" $manifestAbs "构建清单"
    } else {
        $missing.Add([pscustomobject]@{ path = "manifest/build-manifest.json"; reason = "文件不存在：$manifestAbs（发布/构建后才会有）" })
    }
    return [pscustomobject]@{ ProjectId = $projectId; Items = $items; Missing = $missing }
}

function Invoke-Collect([string]$dataAbs, [string]$projectDir, [string[]]$submitLogs, [string]$manifestAbs, [string]$zipPath) {
    $req = New-CollectRequest $dataAbs $projectDir $submitLogs $manifestAbs
    $stage = Join-Path ([System.IO.Path]::GetTempPath()) ("saker-collect-" + [guid]::NewGuid().ToString("N"))
    New-Item -ItemType Directory -Force -Path $stage | Out-Null
    try {
        $entries = [System.Collections.Generic.List[object]]::new()
        $notes = [System.Collections.Generic.List[string]]::new()
        foreach ($item in $req.Items) {
            $dest = Join-Path $stage $item.dest
            $note = Copy-StableFile $item.source $dest
            if ($note) { $notes.Add("$($item.dest)：$note") }
            $entries.Add([pscustomobject]@{
                    path   = ($item.dest -replace '\\', '/')
                    bytes  = (Get-Item -LiteralPath $dest).Length
                    sha256 = (Get-Sha256 $dest)
                    source = $item.source
                })
        }
        $ledgerSide = @($req.Items | Where-Object { $_.dest -like "ledger.sqlite*" } | ForEach-Object { $_.dest })
        $manifestInfo = $null
        $manifestCopy = Join-Path $stage "manifest/build-manifest.json"
        if (Test-Path -LiteralPath $manifestCopy) {
            try {
                $m = Get-Content -LiteralPath $manifestCopy -Raw | ConvertFrom-Json
                $manifestInfo = [pscustomobject]@{
                    version   = $m.version
                    commit    = $m.source.commit
                    dirty     = $m.source.dirty
                    generated = $m.generated_at
                }
            } catch {
                $notes.Add("manifest/build-manifest.json：不是合法 JSON（$($_.Exception.Message)）")
            }
        }
        $collect = [pscustomobject]@{
            collected_at = (Get-Date).ToUniversalTime().ToString("yyyy-MM-ddTHH:mm:ssZ")
            host         = [System.Net.Dns]::GetHostName()
            platform     = [System.Runtime.InteropServices.RuntimeInformation]::OSDescription
            pwsh         = $PSVersionTable.PSVersion.ToString()
            repo_root    = $root
            data_dir     = $dataAbs
            project_id   = $req.ProjectId
            project_dir  = $projectDir
            ledger       = [pscustomobject]@{
                sidecars = $ledgerSide
                note     = "只读复制：账本在复制时可能仍在写入；已带 -wal/-shm，但不做 checkpoint"
            }
            manifest     = $manifestInfo
            entries      = $entries
            missing      = $req.Missing
            notes        = $notes
        }
        $collect | ConvertTo-Json -Depth 8 | Set-Content -LiteralPath (Join-Path $stage "COLLECT.json") -Encoding utf8
        if ($req.Missing.Count -gt 0) {
            $lines = @("# 缺失清单（$($req.Missing.Count) 项）", "# 每行：相对路径<TAB>原因", "")
            foreach ($m in $req.Missing) { $lines += ("{0}`t{1}" -f $m.path, $m.reason) }
            Set-Content -LiteralPath (Join-Path $stage "MISSING.txt") -Value ($lines -join "`n") -Encoding utf8
        }
        New-Item -ItemType Directory -Force -Path (Split-Path -Parent $zipPath) | Out-Null
        if (Test-Path -LiteralPath $zipPath) { Remove-Item -LiteralPath $zipPath -Force }
        [System.IO.Compression.ZipFile]::CreateFromDirectory($stage, $zipPath, [System.IO.Compression.CompressionLevel]::Optimal, $false)
        return [pscustomobject]@{
            Zip      = $zipPath
            Project  = $req.ProjectId
            Entries  = $entries.Count
            Missing  = $req.Missing
            Notes    = $notes
        }
    } finally {
        if (Test-Path -LiteralPath $stage) { Remove-Item -LiteralPath $stage -Recurse -Force -ErrorAction SilentlyContinue }
    }
}

function Invoke-SelfTest {
    $failures = [System.Collections.Generic.List[string]]::new()
    # Count the assertions instead of hardcoding the number: the last version
    # printed "17/17" while the loop over expected entries alone already made 9
    # of them (actual 21), which is exactly the sort of self-report that hides a
    # branch that never ran.
    $script:stChecks = 0
    function Assert-True([bool]$cond, [string]$what) {
        $script:stChecks++
        if (-not $cond) { $failures.Add($what) }
    }
    $fixture = Join-Path ([System.IO.Path]::GetTempPath()) ("saker-collect-selftest-" + [guid]::NewGuid().ToString("N"))
    try {
        $data = Join-Path $fixture "data"
        $proj = Join-Path $data "projects/p-selftest"
        New-Item -ItemType Directory -Force -Path (Join-Path $proj "runs") | Out-Null
        New-Item -ItemType Directory -Force -Path (Join-Path $fixture ".build") | Out-Null
        Set-Content -LiteralPath (Join-Path $proj "ledger.sqlite") -Value "not-a-real-sqlite" -NoNewline
        Set-Content -LiteralPath (Join-Path $proj "runs/r_1.jsonl") -Value '{"event":"a"}' -NoNewline
        Set-Content -LiteralPath (Join-Path $proj "runs/r_2.jsonl") -Value '{"event":"b"}' -NoNewline
        Set-Content -LiteralPath (Join-Path $proj "decisions.jsonl") -Value '{"d":1}' -NoNewline
        Set-Content -LiteralPath (Join-Path $proj "project.json") -Value '{"id":"p-selftest"}' -NoNewline
        Set-Content -LiteralPath (Join-Path $proj "state.md") -Value "# state" -NoNewline
        Set-Content -LiteralPath (Join-Path $fixture ".build/build-manifest.json") `
            -Value '{"version":"1.0.0","source":{"commit":"deadbeef","dirty":false},"generated_at":"2026-11-22T00:00:00Z"}' -NoNewline
        $ledgerShaBefore = Get-Sha256 (Join-Path $proj "ledger.sqlite")

        # Pass 1: operator-actions.jsonl and the submitter log are absent.
        $zip1 = Join-Path $fixture "out/one.zip"
        $r1 = Invoke-Collect $data $proj @() (Join-Path $fixture ".build/build-manifest.json") $zip1
        Assert-True (Test-Path -LiteralPath $zip1) "第一次收包没有产出 zip"
        Assert-True ($r1.Entries -ge 7) "第一次收包条目太少：$($r1.Entries)"
        Assert-True ($r1.Missing.Count -ge 2) "第一次收包应记录缺 operator-actions 与提交器日志，实际 $($r1.Missing.Count)"
        $zip1Entries = [System.IO.Compression.ZipFile]::OpenRead($zip1)
        try {
            $names = @($zip1Entries.Entries | ForEach-Object { $_.FullName })
        } finally { $zip1Entries.Dispose() }
        foreach ($want in @("ledger.sqlite", "runs/r_1.jsonl", "runs/r_2.jsonl", "decisions.jsonl", "project.json", "state.md", "manifest/build-manifest.json", "COLLECT.json", "MISSING.txt")) {
            Assert-True ($names -contains $want) "第一次收包缺少 $want（实际：$($names -join ', ')）"
        }
        Assert-True (-not ($names -contains "operator-actions.jsonl")) "缺失文件不应被伪造进包"

        $tempExtract = Join-Path $fixture "extract"
        [System.IO.Compression.ZipFile]::ExtractToDirectory($zip1, $tempExtract)
        $missingText = Get-Content -LiteralPath (Join-Path $tempExtract "MISSING.txt") -Raw
        Assert-True ($missingText -match "operator-actions\.jsonl") "MISSING.txt 没写 operator-actions.jsonl"
        Assert-True ($missingText -match "提交器日志") "MISSING.txt 没写提交器日志缺失"
        $collectJson = Get-Content -LiteralPath (Join-Path $tempExtract "COLLECT.json") -Raw | ConvertFrom-Json
        Assert-True ($collectJson.project_id -eq "p-selftest") "COLLECT.json 项目 id 不对：$($collectJson.project_id)"
        Assert-True ($collectJson.manifest.version -eq "1.0.0") "COLLECT.json 没带出清单版本"

        # Pass 2: both optional inputs exist -> no MISSING.txt, submit/ included.
        Set-Content -LiteralPath (Join-Path $proj "operator-actions.jsonl") -Value '{"action":"goal_met"}' -NoNewline
        $submit = Join-Path $proj "submissions.jsonl"
        Set-Content -LiteralPath $submit -Value '{"unique_code":"c1","correct":true}' -NoNewline
        $zip2 = Join-Path $fixture "out/two.zip"
        $r2 = Invoke-Collect $data $proj @($submit) (Join-Path $fixture ".build/build-manifest.json") $zip2
        $zip2Entries = [System.IO.Compression.ZipFile]::OpenRead($zip2)
        try { $names2 = @($zip2Entries.Entries | ForEach-Object { $_.FullName }) } finally { $zip2Entries.Dispose() }
        Assert-True ($names2 -contains "operator-actions.jsonl") "第二次收包缺 operator-actions.jsonl"
        Assert-True ($names2 -contains "submit/submissions.jsonl") "第二次收包缺提交器日志"
        Assert-True (-not ($names2 -contains "MISSING.txt")) "没有缺失时不应写 MISSING.txt"

        # Read-only proof: the source ledger bytes are unchanged by collection.
        $ledgerShaAfter = Get-Sha256 (Join-Path $proj "ledger.sqlite")
        Assert-True ($ledgerShaBefore -eq $ledgerShaAfter) "收包改动了账本字节"

    } finally {
        if (Test-Path -LiteralPath $fixture) { Remove-Item -LiteralPath $fixture -Recurse -Force -ErrorAction SilentlyContinue }
    }
    if ($failures.Count -gt 0) {
        Write-Host "SELFTEST 失败 $($failures.Count) 项：" -ForegroundColor Red
        foreach ($f in $failures) { Write-Host "  - $f" }
        exit 1
    }
    Write-Host "SELFTEST OK $($script:stChecks)/$($script:stChecks)" -ForegroundColor Green
    exit 0
}

if ($SelfTest) { Invoke-SelfTest }

$dataAbs = Resolve-FromRoot $DataDir
if (-not (Test-Path -LiteralPath (Join-Path $dataAbs "projects"))) {
    [Console]::Error.WriteLine("找不到数据目录：$dataAbs（用 -DataDir 指向实例的 data 目录）")
    exit 1
}
$manifestAbs = Resolve-FromRoot $Manifest
if ($Project) {
    if ([System.IO.Path]::IsPathRooted($Project) -or $Project -match '[\\/]') {
        $projectDir = [System.IO.Path]::GetFullPath($Project)
    } else {
        $projectDir = Join-Path $dataAbs ("projects/" + $Project)
    }
} else {
    $candidates = @(Get-ChildItem -LiteralPath (Join-Path $dataAbs "projects") -Directory |
        Where-Object { Test-Path -LiteralPath (Join-Path $_.FullName "ledger.sqlite") })
    if ($candidates.Count -eq 0) {
        [Console]::Error.WriteLine("数据目录下没有带 ledger.sqlite 的项目：$dataAbs")
        exit 1
    }
    if ($candidates.Count -gt 1) {
        [Console]::Error.WriteLine("有多个项目，请用 -Project 指定一个：$(@($candidates | ForEach-Object { $_.Name }) -join ', ')")
        exit 1
    }
    $projectDir = $candidates[0].FullName
}
if (-not (Test-Path -LiteralPath $projectDir)) {
    [Console]::Error.WriteLine("项目目录不存在：$projectDir")
    exit 1
}
if (-not $Out) {
    $stamp = (Get-Date).ToUniversalTime().ToString("yyyyMMdd-HHmmss")
    $Out = "_build-artifacts/collect/$(Split-Path -Leaf $projectDir)-$stamp.zip"
}
$zipAbs = Resolve-FromRoot $Out

$result = Invoke-Collect $dataAbs $projectDir $SubmitLog $manifestAbs $zipAbs
$size = (Get-Item -LiteralPath $zipAbs).Length
Write-Host ("收包完成：{0}（{1:N1} MB，{2} 个文件，项目 {3}）" -f $zipAbs, ($size / 1MB), $result.Entries, $result.Project)
if ($result.Missing.Count -gt 0) {
    Write-Host ("有 {0} 项缺失，已写入 zip 里的 MISSING.txt：" -f $result.Missing.Count) -ForegroundColor Yellow
    foreach ($m in $result.Missing) { Write-Host ("  - {0}：{1}" -f $m.path, $m.reason) }
    if ($FailOnMissing) { exit 3 }
}
if ($result.Notes.Count -gt 0) {
    Write-Host "复制提示（已记入 COLLECT.json）："
    foreach ($n in $result.Notes) { Write-Host "  - $n" }
}
exit 0
