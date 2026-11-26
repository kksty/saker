# Download a matched rc10 Hub + scan pair. Saker needs its fork's session
# workspace and scheduling extensions; use artifacts built from saker/fixes.
param(
    [Parameter(Mandatory = $true)][string]$Url,
    [Parameter(Mandatory = $true)][string]$ScanUrl,
    [string]$Sha256 = "",
    [string]$ScanSha256 = "",
    [string]$Out = "./bin/cyber-harness.exe",
    [string]$ScanOut = ""
)
$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot
if (-not [IO.Path]::IsPathRooted($Out)) { $Out = Join-Path $root $Out }
if (-not $ScanOut) { $ScanOut = Join-Path (Split-Path -Parent $Out) 'cyber-scan.exe' }
if (-not [IO.Path]::IsPathRooted($ScanOut)) { $ScanOut = Join-Path $root $ScanOut }
$tempRoot = [IO.Path]::GetFullPath([IO.Path]::GetTempPath())
$tmp = [IO.Path]::GetFullPath((Join-Path $tempRoot ('harness-' + [guid]::NewGuid().ToString('N'))))
if (-not $tmp.StartsWith($tempRoot, [StringComparison]::OrdinalIgnoreCase) -or $tmp -eq $tempRoot) { throw '临时路径越界' }
New-Item -ItemType Directory -Path $tmp -Force | Out-Null
try {
    $artifacts = @(
        @{ Url = $Url; Sha = $Sha256; Name = 'cyber-harness'; Out = $Out },
        @{ Url = $ScanUrl; Sha = $ScanSha256; Name = 'cyber-scan'; Out = $ScanOut }
    )
    foreach ($artifact in $artifacts) {
        $zip = Join-Path $tmp ($artifact.Name + '.zip')
        Invoke-WebRequest -Uri $artifact.Url -OutFile $zip -UseBasicParsing
        if ($artifact.Sha -and (Get-FileHash -LiteralPath $zip -Algorithm SHA256).Hash -ne $artifact.Sha) { throw ('sha256 不匹配：' + $artifact.Name) }
        $extract = Join-Path $tmp $artifact.Name
        Expand-Archive -LiteralPath $zip -DestinationPath $extract
        $bins = @(Get-ChildItem -LiteralPath $extract -Recurse -File | Where-Object { $_.Name -eq ($artifact.Name + '.exe') })
        if ($bins.Count -ne 1) { throw ('压缩包必须包含唯一的 ' + $artifact.Name + '.exe') }
        $artifact['Bin'] = $bins[0].FullName
    }
    foreach ($artifact in $artifacts) {
        New-Item -ItemType Directory -Path (Split-Path -Parent $artifact.Out) -Force | Out-Null
        Copy-Item -LiteralPath $artifact.Bin -Destination $artifact.Out -Force
        Write-Host ('ok: ' + $artifact.Out)
    }
} finally { Remove-Item -LiteralPath $tmp -Recurse -Force -ErrorAction SilentlyContinue }
