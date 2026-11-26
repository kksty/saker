package platform

import (
	"reflect"
	"runtime"
	"testing"
)

// The descriptor is the one place that spells "what is a binary called here".
// Both semantics are asserted from the same test on both hosts: a test that
// only ran on Windows could not catch a Linux regression, and vice versa.
func TestDescribeBothPlatforms(t *testing.T) {
	cases := []struct {
		goos, goarch string
		family       string
		exeSuffix    string
		sep, listSep string
		caseFolded   bool
	}{
		{"windows", "amd64", "windows", ".exe", `\`, ";", true},
		{"windows", "arm64", "windows", ".exe", `\`, ";", true},
		{"linux", "amd64", "posix", "", "/", ":", false},
		{"darwin", "arm64", "posix", "", "/", ":", false},
		{"plan9", "386", "posix", "", "/", ":", false},
	}
	for _, c := range cases {
		d := Describe(c.goos, c.goarch)
		if d.GOOS != c.goos || d.GOARCH != c.goarch || d.Family != c.family ||
			d.EXESuffix != c.exeSuffix || d.PathSeparator != c.sep ||
			d.PathListSeparator != c.listSep || d.CaseInsensitivePaths != c.caseFolded {
			t.Fatalf("Describe(%q,%q) = %+v", c.goos, c.goarch, d)
		}
	}
	if Current().GOOS != runtime.GOOS || Current().GOARCH != runtime.GOARCH {
		t.Fatalf("Current() = %+v, runtime says %s/%s", Current(), runtime.GOOS, runtime.GOARCH)
	}
	if got := ExecutableName("windows", "cyber-harness"); got != "cyber-harness.exe" {
		t.Fatalf("windows name = %q", got)
	}
	if got := ExecutableName("windows", "cyber-harness.exe"); got != "cyber-harness.exe" {
		t.Fatalf("windows name doubled the suffix: %q", got)
	}
	if got := ExecutableName("windows", "CYBER-HARNESS.EXE"); got != "CYBER-HARNESS.EXE" {
		t.Fatalf("windows name should keep the caller's spelling: %q", got)
	}
	if got := ExecutableName("linux", "cyber-harness"); got != "cyber-harness" {
		t.Fatalf("linux name = %q", got)
	}
	if got := ExecutableName("linux", "cyber-harness.exe"); got != "cyber-harness.exe" {
		t.Fatalf("linux must not strip a literal name: %q", got)
	}
	if got := ExecutableName("linux", "  "); got != "" {
		t.Fatalf("blank name = %q", got)
	}
	if got := ExecutableCandidates("windows", "saker"); !reflect.DeepEqual(got, []string{"saker.exe", "saker"}) {
		t.Fatalf("windows candidates = %v", got)
	}
	if got := ExecutableCandidates("windows", "saker.exe"); !reflect.DeepEqual(got, []string{"saker.exe"}) {
		t.Fatalf("windows .exe candidates = %v", got)
	}
	if got := ExecutableCandidates("linux", "saker"); !reflect.DeepEqual(got, []string{"saker"}) {
		t.Fatalf("linux candidates = %v", got)
	}
	if got := TrimExecutableSuffix("AISCAN.EXE"); got != "AISCAN" {
		t.Fatalf("trim = %q", got)
	}
	if got := TrimExecutableSuffix("aiscan"); got != "aiscan" {
		t.Fatalf("trim without suffix = %q", got)
	}
}

// A process table on either platform may name a path with either separator; the
// basename rule is a deliberate equivalence, so it is asserted for both forms
// regardless of the host running the test.
func TestExecutableBaseAcceptsBothSeparators(t *testing.T) {
	cases := map[string]string{
		`C:\tools\cyber-harness.exe`: "cyber-harness.exe",
		"/opt/cyber/cyber-scan":      "cyber-scan",
		`C:\mixed/cyber-scan.exe`:    "cyber-scan.exe",
		"  aiscan-full.exe  ":        "aiscan-full.exe",
		"aiscan":                     "aiscan",
		"":                           "",
	}
	for in, want := range cases {
		if got := ExecutableBase(in); got != want {
			t.Fatalf("ExecutableBase(%q) = %q, want %q", in, got, want)
		}
	}
}
