package platform

import (
	"reflect"
	"testing"
)

// Both platform path rules are exercised from the same test on the same host.
// That is the point of taking GOOS explicitly: a Linux runner proves the
// Windows equivalence instead of skipping it.
func TestNormalizePathBothPlatforms(t *testing.T) {
	windows := []struct{ in, want string }{
		{`C:\Data\..\Temp`, `C:\Temp`},
		{`c:/program files/x`, `c:\program files\x`},
		{`C:\a\\b\`, `C:\a\b`},
		{`C:\`, `C:\`},
		{`C:`, `C:`},
		{`C:\..\..`, `C:\`},
		{`.\a\b`, `a\b`},
		{`..\a`, `..\a`},
		{`\\server\share\a\..\b`, `\\server\share\b`},
		{`\\server\share`, `\\server\share`},
		{``, ``},
	}
	for _, c := range windows {
		if got := NormalizePath("windows", c.in); got != c.want {
			t.Fatalf("windows NormalizePath(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	posix := []struct{ in, want string }{
		{"/srv/data/../data-a", "/srv/data-a"},
		{"/srv//data-a/", "/srv/data-a"},
		{`/srv/data-a\sub`, `/srv/data-a\sub`}, // backslash is a file-name byte
		{"./a/./b", "a/b"},
		{"a", "a"},
		{"", ""},
	}
	for _, c := range posix {
		if got := NormalizePath("linux", c.in); got != c.want {
			t.Fatalf("linux NormalizePath(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestPathKeyCaseRules(t *testing.T) {
	if got := PathKey("windows", `C:\Data\Host`); got != `c:\data\host` {
		t.Fatalf("windows key = %q", got)
	}
	if got := PathKey("linux", "/srv/Data/Host"); got != "/srv/Data/Host" {
		t.Fatalf("linux key was case-folded: %q", got)
	}
	if !SamePath("windows", `C:\Data`, `c:/data/`) {
		t.Fatal("windows: differently spelled path did not compare equal")
	}
	if SamePath("linux", "/srv/Data", "/srv/data") {
		t.Fatal("linux: paths differing only by case must not compare equal")
	}
	if SamePath("linux", "", "") || SamePath("windows", ".", "") {
		t.Fatal("empty paths must never compare equal")
	}
	if SamePath("windows", `C:\a`, `D:\a`) {
		t.Fatal("different drives compared equal")
	}
}

func TestIsAbsoluteBothPlatforms(t *testing.T) {
	cases := []struct {
		goos, p string
		want    bool
	}{
		{"windows", `C:\x`, true},
		{"windows", `c:/x`, true},
		{"windows", `\\server\share`, true},
		{"windows", `\x`, true},
		{"windows", `C:x`, false},
		{"windows", `x`, false},
		{"linux", "/srv/x", true},
		{"linux", "srv/x", false},
		{"linux", `C:\x`, false},
	}
	for _, c := range cases {
		if got := IsAbsolute(c.goos, c.p); got != c.want {
			t.Fatalf("IsAbsolute(%q,%q) = %v, want %v", c.goos, c.p, got, c.want)
		}
	}
}

func TestSplitListBothPlatforms(t *testing.T) {
	if got := SplitList("windows", `C:\a;C:\b`); !reflect.DeepEqual(got, []string{`C:\a`, `C:\b`}) {
		t.Fatalf("windows list = %v", got)
	}
	if got := SplitList("linux", "/a:/b"); !reflect.DeepEqual(got, []string{"/a", "/b"}) {
		t.Fatalf("linux list = %v", got)
	}
	if got := SplitList("windows", ""); got != nil {
		t.Fatalf("empty list = %v", got)
	}
}

// The orphan sweep's identity test is the highest-risk path rule: killing a
// process because a *prefix* matched would take down another instance. Both
// platform spellings are asserted from one test.
func TestContainsPathSegmentBothPlatforms(t *testing.T) {
	windows := []struct {
		haystack, needle string
		want             bool
	}{
		{`"C:\tools\cyber-harness.exe" agent --db C:\instances\data-a\host\cyber-web.db`, `C:\instances\data-a`, true},
		{`"C:\tools\cyber-harness.exe" agent --db C:\instances\data-ab\host\cyber-web.db`, `C:\instances\data-a`, false},
		{`cyber-scan.exe --data "C:\Program Files\saker\data-a"`, `C:\Program Files\saker\data-a`, true},
		{`--db=c:\INSTANCES\DATA-A\host.db`, `C:\instances\data-a`, true},
		{`--config C:\instances\data-a2\x.yaml`, `C:\instances\data-a`, false},
		{`--db C:\instances\data-a`, ``, false},
		{`--db C:\instances\data-a`, `.`, false},
	}
	for _, c := range windows {
		if got := ContainsPathSegment("windows", c.haystack, c.needle); got != c.want {
			t.Fatalf("windows ContainsPathSegment(%q,%q) = %v, want %v", c.haystack, c.needle, got, c.want)
		}
	}
	linux := []struct {
		haystack, needle string
		want             bool
	}{
		{"cyber-scan agent --data /srv/instances/data-a --host 127.0.0.1", "/srv/instances/data-a", true},
		{"cyber-scan agent --data /srv/instances/data-ab", "/srv/instances/data-a", false},
		{"cyber-scan --data=/srv/instances/data-a/", "/srv/instances/data-a", true},
		{"cyber-scan --config /srv/instances/Data-A/x.yaml", "/srv/instances/data-a", false},
	}
	for _, c := range linux {
		if got := ContainsPathSegment("linux", c.haystack, c.needle); got != c.want {
			t.Fatalf("linux ContainsPathSegment(%q,%q) = %v, want %v", c.haystack, c.needle, got, c.want)
		}
	}
}

func TestIsPathBoundary(t *testing.T) {
	for _, b := range []byte{'\\', '/', '"', '\'', '=', ' ', '\t', '\r', '\n', ';', ','} {
		if !IsPathBoundary(b) {
			t.Fatalf("%q should end a path in a command line", b)
		}
	}
	for _, b := range []byte{'a', '0', '-', '_', '.'} {
		if IsPathBoundary(b) {
			t.Fatalf("%q must not end a path", b)
		}
	}
}
