#!/usr/bin/env sh
# Install locally built saker programs from a candidate directory into bin/.
#
# This is the POSIX counterpart of scripts/install-local.ps1 and has the same
# contract:
#   * only the four programs under bin/ are replaced (saker, cyber-harness,
#     cyber-scan, saker-repair);
#   * no service is started or restarted;
#   * runtime data (data/, projects, ledgers, evidence) is never read or written.
#
# The candidate directory defaults to .build/release relative to the repository
# root. Files are copied to a temporary name, hash-verified, and only then moved
# into place; a failed verification leaves the current bin/ untouched.
#
# Usage:
#   scripts/install-local.sh [--from <dir>]
#   scripts/install-local.sh --help
set -eu

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
from=".build/release"

usage() {
  cat <<'EOF'
用法: scripts/install-local.sh [--from <候选目录>]

把候选目录（默认 .build/release）里的 saker、cyber-harness、cyber-scan、
saker-repair 安装到 bin/。只替换 bin/ 下的程序：不启动服务，不读写运行数据。
EOF
}

while [ $# -gt 0 ]; do
  case "$1" in
    --from|-From)
      [ $# -ge 2 ] || { echo "--from 需要一个目录参数" >&2; exit 2; }
      from=$2
      shift 2
      ;;
    --help|-h)
      usage
      exit 0
      ;;
    *)
      echo "未知参数: $1（可用 --help）" >&2
      exit 2
      ;;
  esac
done

case "$from" in
  /*) ;;
  *) from="$root/$from" ;;
esac
if [ ! -d "$from" ]; then
  echo "找不到候选目录：$from" >&2
  exit 1
fi
source_dir=$(CDPATH= cd -- "$from" && pwd -P)
install_bin="$root/bin"

names="saker cyber-harness cyber-scan saker-repair"

# 1) Refuse to replace a program that is still running. The Windows script uses
# WMI; here the process table is read directly on Linux (/proc) and through
# pgrep elsewhere. If neither is available the check is skipped with a warning:
# an install over a running control plane would leave mixed binaries.
running=""
if [ -d /proc ] && [ -r /proc/self/exe ]; then
  for proc in /proc/[0-9]*; do
    [ -e "$proc/exe" ] || continue
    exe=$(readlink "$proc/exe" 2>/dev/null || true)
    [ -n "$exe" ] || continue
    exe=${exe% (deleted)}
    for name in $names; do
      if [ "$exe" = "$install_bin/$name" ]; then
        running="$running ${proc#/proc/}"
      fi
    done
  done
elif command -v pgrep >/dev/null 2>&1; then
  for name in $names; do
    if pgrep -x "$name" >/dev/null 2>&1; then
      running="$running $name"
    fi
  done
else
  echo "警告：无法检查运行中的程序（没有 /proc 也没有 pgrep）；请在停止平台后再安装。" >&2
fi
if [ -n "$running" ]; then
  echo "旧程序仍在运行，请先停止平台后重试。PID/进程:${running}" >&2
  exit 1
fi

# 2) Validate every candidate before touching bin/.
for name in $names; do
  if [ ! -f "$source_dir/$name" ]; then
    echo "缺少候选程序：$name（候选目录 $source_dir）" >&2
    exit 1
  fi
done
if [ "$source_dir" = "$install_bin" ]; then
  echo "候选目录不能是安装目录 bin" >&2
  exit 1
fi
# The same directory can be spelled logically and physically (symlinked /tmp,
# macOS /private/tmp): compare the resolved forms too.
if [ -d "$install_bin" ]; then
  install_real=$(CDPATH= cd -- "$install_bin" && pwd -P)
  if [ "$source_dir" = "$install_real" ]; then
    echo "候选目录不能是安装目录 bin" >&2
    exit 1
  fi
fi

sha256() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" | awk '{print $1}'
  elif command -v openssl >/dev/null 2>&1; then
    openssl dgst -sha256 "$1" | awk '{print $NF}'
  else
    echo "找不到 sha256 工具（sha256sum/shasum/openssl）" >&2
    return 1
  fi
}

mkdir -p "$install_bin"

# 3) Stage and verify each program under bin/<name>.new.
for name in $names; do
  candidate="$source_dir/$name"
  pending="$install_bin/$name.new"
  cp "$candidate" "$pending"
  before=$(sha256 "$candidate")
  after=$(sha256 "$pending")
  if [ "$before" != "$after" ]; then
    rm -f "$pending"
    echo "候选程序校验失败：$name" >&2
    exit 1
  fi
done

# 4) Swap them into place (the .new files are complete and verified).
for name in $names; do
  chmod 0755 "$install_bin/$name.new"
  mv -f "$install_bin/$name.new" "$install_bin/$name"
done

# 5) Post-install verification: the installed bytes must be the candidate bytes.
for name in $names; do
  if [ "$(sha256 "$source_dir/$name")" != "$(sha256 "$install_bin/$name")" ]; then
    echo "安装校验失败：$name" >&2
    exit 1
  fi
done

# 6) Remove stale local build leftovers at the top level of .build, never
# directories, toolchains, project data or the release pair used above (same
# eligibility rule as the PowerShell script).
build_dir="$root/.build"
if [ -d "$build_dir" ]; then
  # Physical path: /tmp is a symlinked mount in some shells (Git Bash), so the
  # boundary check below must compare like with like.
  build_real=$(CDPATH= cd -- "$build_dir" && pwd -P)
  for stale in "$build_dir"/*; do
    [ -f "$stale" ] || continue
    case "$(basename "$stale")" in
      saker*|aiscan*) ;;
      *) continue ;;
    esac
    stale_dir=$(CDPATH= cd -- "$(dirname -- "$stale")" && pwd -P)
    [ "$stale_dir" = "$build_real" ] || { echo "旧构建路径越界：$stale" >&2; exit 1; }
    [ "$stale_dir" = "$source_dir" ] && continue
    rm -f "$stale"
  done
  for stale in "$install_bin/aiscan-full.exe.bak" "$build_dir/harness/aiscan-full.exe" "$install_bin/aiscan-full.exe"; do
    if [ -f "$stale" ]; then rm -f "$stale"; fi
  done
fi

echo "已安装：$install_bin（未启动服务）"
