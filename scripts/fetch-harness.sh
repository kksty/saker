#!/usr/bin/env sh
# Download matched zip artifacts built from saker/fixes (Hub + scan node).
set -eu
[ $# -ge 2 ] || { echo "usage: $0 <hub-zip-url> <scan-zip-url> [hub-sha256] [scan-sha256]" >&2; exit 1; }
root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
fetch() {
  name="$1"; url="$2"; sha="$3"
  zip="$tmp/$name.zip"
  if command -v curl >/dev/null 2>&1; then curl -fsSL "$url" -o "$zip"; else wget -q "$url" -O "$zip"; fi
  if [ -n "$sha" ]; then
    if command -v sha256sum >/dev/null 2>&1; then actual=$(sha256sum "$zip" | awk '{print $1}'); else actual=$(shasum -a 256 "$zip" | awk '{print $1}'); fi
    [ "$actual" = "$sha" ] || { echo "sha256 mismatch: $name" >&2; exit 1; }
  fi
  mkdir "$tmp/$name"
  unzip -q "$zip" -d "$tmp/$name"
  find "$tmp/$name" -type f -name "$name" > "$tmp/$name.paths"
  [ "$(wc -l < "$tmp/$name.paths" | tr -d ' ')" = 1 ] || { echo "archive must contain exactly one $name" >&2; exit 1; }
}
fetch cyber-harness "$1" "${3:-}"
fetch cyber-scan "$2" "${4:-}"
mkdir -p "$root/bin"
for name in cyber-harness cyber-scan; do
  cp "$(cat "$tmp/$name.paths")" "$root/bin/$name"
  chmod +x "$root/bin/$name"
  echo "ok: $root/bin/$name"
done
