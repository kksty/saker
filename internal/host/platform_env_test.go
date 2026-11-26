package host

import (
	"strings"
	"testing"
)

// These environment assertions are deliberately platform-neutral and run on
// both Windows and Linux. Platform-specific PATH work is covered by the
// build-tagged tests next to this file (platform_env_windows_test.go /
// platform_env_posix_test.go) with the same semantic on both sides.

// A control plane's own environment is inherited by the host; the sweep must not
// silently drop unrelated variables (the model/gateway configuration can rely on
// them).
func TestChildEnvKeepsUnrelatedVariables(t *testing.T) {
	t.Setenv("SAKER_PLATFORM_TEST", "kept")
	h := &Host{cfg: Config{Logf: func(string, ...any) {}}}
	if got := envValue(h.childEnv(nil), "SAKER_PLATFORM_TEST"); got != "kept" {
		t.Fatalf("unrelated variable = %q", got)
	}
}

// Dropping must be limited to the configured prefixes: an instance that drops
// CYBER_* keeps everything else, including a value that merely contains it.
func TestChildEnvDropsOnlyConfiguredPrefixes(t *testing.T) {
	t.Setenv("CYBER_API_KEY", "secret")
	t.Setenv("MY_CYBER_API_KEY", "not-a-cyber-prefix")
	h := &Host{cfg: Config{
		Logf:            func(string, ...any) {},
		DropEnvPrefixes: []string{"CYBER_"},
		Env:             []string{"OTHER_SETTING=1"},
	}}
	env := h.childEnv(h.cfg.Env)
	if got := envValue(env, "CYBER_API_KEY"); got != "" {
		t.Fatalf("dropped prefix survived: %q", got)
	}
	if got := envValue(env, "MY_CYBER_API_KEY"); got != "not-a-cyber-prefix" {
		t.Fatalf("non-prefix variable was dropped: %q", got)
	}
	if got := envValue(env, "OTHER_SETTING"); got != "1" {
		t.Fatalf("extra setting = %q", got)
	}
}

// Settings are "K=V"; a malformed entry is ignored rather than becoming a
// variable named after the whole string.
func TestChildEnvIgnoresMalformedSettings(t *testing.T) {
	h := &Host{cfg: Config{
		Logf: func(string, ...any) {},
		Env:  []string{"no-equals-sign", "GOOD=1"},
	}}
	env := h.childEnv(h.cfg.Env)
	if got := envValue(env, "GOOD"); got != "1" {
		t.Fatalf("good setting = %q", got)
	}
	for _, kv := range env {
		if kv == "no-equals-sign" {
			t.Fatalf("malformed setting leaked into the child environment: %v", env)
		}
	}
}

// setEnv keeps exactly one definition so a child does not have to guess between
// "Path=" and "PATH="; this property is what the Windows PATH fixup relies on.
func TestSetEnvKeepsSingleDefinitionOnBothPlatforms(t *testing.T) {
	env := []string{"Path=/a", "PATH=/b", "OTHER=1"}
	setEnv(&env, "path", "/c")
	got := envValue(env, "PATH")
	if got != "/c" {
		t.Fatalf("PATH = %q", got)
	}
	n := 0
	for _, kv := range env {
		if k, _, ok := strings.Cut(kv, "="); ok && strings.EqualFold(k, "PATH") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("PATH defined %d times: %v", n, env)
	}
}

// The host log line must not contain the console token; lineWriter masks it.
// This is a cross-platform property of the same code path.
func TestChildEnvCarriesSettingsOverridesOnce(t *testing.T) {
	t.Setenv("SAKER_OVERRIDE", "inherited")
	h := &Host{cfg: Config{
		Logf: func(string, ...any) {},
		Env:  []string{"SAKER_OVERRIDE=instance"},
	}}
	env := h.childEnv(h.cfg.Env)
	if got := envValue(env, "SAKER_OVERRIDE"); got != "instance" {
		t.Fatalf("override = %q", got)
	}
	n := 0
	for _, kv := range env {
		if strings.HasPrefix(kv, "SAKER_OVERRIDE=") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("override appears %d times", n)
	}
}
