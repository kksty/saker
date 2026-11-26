package main

import (
	"os"
	"path/filepath"
	"strings"
)

// sessionEnvScript is sourced by every non-interactive POSIX shell the harness
// runs (BASH_ENV, and ENV for sh). It carries no credential: it locates the
// session's case from the working directory and exports the session's token and
// base URLs, so a model can use $SAKER_AUTH / $SAKER_BATTLE / $SAKER_API directly instead
// of reading and exporting files by hand. A missing script only costs that
// convenience; .tw/auth stays the fallback.
//
// SAKER_BIN is the running saker executable, exported so a model can call the native
// read verbs (`"$SAKER_BIN" board`, `"$SAKER_BIN" wake`) instead of hand-writing curl.
// The path is substituted at install time (writeSessionEnv): it differs per
// machine and a stale path from .tw must never shadow it.
const sessionEnvScript = `# saker session environment (no secrets in this file).
SAKER_BIN=@SAKER_BIN@
export SAKER_BIN
_saker_dir=""
_saker_walk="$PWD"
while [ -n "$_saker_walk" ]; do
    if [ -f "$_saker_walk/.tw/identity.json" ]; then _saker_dir="$_saker_walk"; break; fi
    [ "$_saker_walk" = "/" ] && break
    _saker_walk=$(dirname "$_saker_walk")
done
if [ -n "$_saker_dir" ]; then
    _saker_id="$_saker_dir/.tw/identity.json"
    SAKER_API=$(sed -n 's/.*"api_base": *"\([^"]*\)".*/\1/p' "$_saker_id")
    SAKER_PROJECT=$(sed -n 's/.*"project_id": *"\([^"]*\)".*/\1/p' "$_saker_id")
    SAKER_SELF=$(sed -n 's/.*"intent_id": *"\([^"]*\)".*/\1/p' "$_saker_id")
    SAKER_AGENT="$SAKER_API/agent/$SAKER_PROJECT/api"
    SAKER_BATTLE="$SAKER_API/p/$SAKER_PROJECT/api"
    if [ -f "$_saker_dir/.tw/auth" ]; then
        SAKER_AUTH=$(cat "$_saker_dir/.tw/auth")
    else
        unset SAKER_AUTH
    fi
    export SAKER_API SAKER_PROJECT SAKER_SELF SAKER_AGENT SAKER_BATTLE SAKER_AUTH
fi
unset _saker_dir _saker_walk _saker_id
`

// twBinPlaceholder is replaced with the shell-quoted executable path. It is not
// inside quotes in the template: the quoting is part of the substitution.
const twBinPlaceholder = "@SAKER_BIN@"

// sessionEnvContent renders the shell init for this executable. os.Executable
// failure leaves SAKER_BIN empty: the verb still works with --base/--token, and the
// skill's curl fallback covers the rest.
func sessionEnvContent() string {
	bin := ""
	if p, err := os.Executable(); err == nil {
		if abs, absErr := filepath.Abs(p); absErr == nil {
			p = abs
		}
		// Git Bash on Windows takes forward slashes; a backslash path is read as
		// escapes. ToSlash is a no-op on POSIX.
		bin = filepath.ToSlash(p)
	}
	return strings.Replace(sessionEnvScript, twBinPlaceholder, shellSingleQuote(bin), 1)
}

// shellSingleQuote wraps s in single quotes for a POSIX shell, escaping any
// embedded quote the standard way. Empty stays an explicit empty string.
func shellSingleQuote(s string) string {
	if s == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// sessionEnvPath is where the host's shell init lives. It is instance state,
// not per project: the script resolves the caller's case from $PWD.
func sessionEnvPath(dataDir string) string {
	return filepath.Join(dataDir, "harness", "session-env.sh")
}

// writeSessionEnv installs the shell init and returns its path. It rewrites only
// when the content changed, like the skill installer; the content embeds this
// binary's path, so a moved/rebuilt saker updates it exactly once.
func writeSessionEnv(dataDir string) (string, error) {
	path := sessionEnvPath(dataDir)
	content := sessionEnvContent()
	if cur, err := os.ReadFile(path); err == nil && string(cur) == content {
		return path, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return "", err
	}
	return path, nil
}
