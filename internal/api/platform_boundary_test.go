package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"

	"github.com/kksty/saker/internal/platform"
)

// boundaryOnMux fetches the boundary document from a mux built exactly like the
// hub builds the participant surface.
func boundaryOnMux(t *testing.T, register func(*http.ServeMux)) platform.BoundaryReport {
	t.Helper()
	mux := http.NewServeMux()
	register(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/platform-boundary", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var got platform.BoundaryReport
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v; body = %s", err, rec.Body.String())
	}
	return got
}

// The endpoint is the machine-readable form of the documented limitation:
// process and network isolation are unsupported_boundary on both platforms,
// never "enforced". This test runs on Windows and Linux with the same assertion.
func TestPlatformBoundaryReportsUnenforcedIsolation(t *testing.T) {
	s := New(nil, nil, t.TempDir(), t.TempDir())
	got := boundaryOnMux(t, s.registerAgent)

	if got.Schema != platform.BoundarySchema {
		t.Fatalf("schema = %q, want %q", got.Schema, platform.BoundarySchema)
	}
	if got.Platform.GOOS != runtime.GOOS || got.Platform.GOARCH != runtime.GOARCH {
		t.Fatalf("platform = %+v, want %s/%s", got.Platform, runtime.GOOS, runtime.GOARCH)
	}
	byID := map[string]platform.Boundary{}
	for _, b := range got.Isolation {
		byID[b.ID] = b
	}
	for _, id := range []string{"process", "network"} {
		b, ok := byID[id]
		if !ok {
			t.Fatalf("isolation %q missing: %+v", id, got.Isolation)
		}
		if b.Status != platform.StatusUnsupportedBoundary {
			t.Fatalf("%s status = %q, want %q", id, b.Status, platform.StatusUnsupportedBoundary)
		}
		if b.Enforced {
			t.Fatalf("%s is claimed as enforced", id)
		}
		if strings.TrimSpace(b.Detail) == "" {
			t.Fatalf("%s has no explanation", id)
		}
	}
	if len(got.Limitations) == 0 {
		t.Fatal("no documented limitations")
	}
}

// The same read-only document is registered on the console mux. A second route
// on the other surface must not have a different answer: both handlers read the
// same pure function.
func TestPlatformBoundaryIsReadOnlyOnBothMuxes(t *testing.T) {
	s := New(nil, nil, t.TempDir(), t.TempDir())

	for name, register := range map[string]func(*http.ServeMux){
		"participant": s.registerAgent,
		"console":     s.Register,
	} {
		mux := http.NewServeMux()
		register(mux)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/platform-boundary", nil))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s: POST status = %d, want 405 (read-only)", name, rec.Code)
		}
		if body := boundaryOnMux(t, register); !strings.Contains(strings.Join(body.Limitations, " "), "同等支持") {
			t.Fatalf("%s: limitations do not state equal support: %+v", name, body.Limitations)
		}
	}
}

// The boundary document is pure status: two calls must agree byte-for-byte, so
// a status page cannot drift from the declaration in internal/platform.
func TestPlatformBoundaryIsStable(t *testing.T) {
	s := New(nil, nil, t.TempDir(), t.TempDir())
	first := boundaryOnMux(t, s.registerAgent)
	second := boundaryOnMux(t, s.registerAgent)
	a, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(second)
	if err != nil {
		t.Fatal(err)
	}
	if string(a) != string(b) {
		t.Fatalf("unstable boundary document:\n%s\n%s", a, b)
	}
}
