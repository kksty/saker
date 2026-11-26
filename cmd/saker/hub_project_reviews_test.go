package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// PUT /api/projects/{id} carries the project's own review switch, and the write
// must reach the running project in the same call. A manifest-only write would
// answer 200 while a running project kept the old switch until the next restart,
// which is exactly the "looks configurable, does nothing" state this endpoint
// must not have.
func TestProjectReviewSwitchWriteAppliesLive(t *testing.T) {
	f := newInstanceReviewsHub(t, false) // instance default: reviews open
	p, k := f.attached(t, "Acme", "acme.example")
	if k.srv.ReviewsDisabled() {
		t.Fatal("fixture: the project should have attached with reviews open")
	}

	code, out := putProject(t, f.h, p.ID, `{"reviews_disabled":true}`)
	if code != http.StatusOK {
		t.Fatalf("close = %d %v", code, out)
	}
	if out["reviews_disabled"] != true || out["reviews_disabled_override"] != true {
		t.Fatalf("close reports %v; want effective/override closed", out)
	}
	// No review was running, so the cancel count is omitted rather than sent as 0.
	if out["reviews_canceled"] != nil {
		t.Fatalf("close with nothing running reports reviews_canceled=%v", out["reviews_canceled"])
	}
	if man := readManifest(t, p); man.ReviewsDisabled == nil || !*man.ReviewsDisabled {
		t.Fatalf("manifest switch = %v; want a stored true", man.ReviewsDisabled)
	}
	if !k.srv.ReviewsDisabled() || !k.srv.ReviewsPaused() {
		t.Fatalf("running project = disabled %v paused %v; want both closed without a restart",
			k.srv.ReviewsDisabled(), k.srv.ReviewsPaused())
	}

	code, out = putProject(t, f.h, p.ID, `{"reviews_disabled":false}`)
	if code != http.StatusOK {
		t.Fatalf("open = %d %v", code, out)
	}
	if out["reviews_disabled"] != false || out["reviews_disabled_override"] != false {
		t.Fatalf("open reports %v; want effective/override open", out)
	}
	if man := readManifest(t, p); man.ReviewsDisabled == nil || *man.ReviewsDisabled {
		t.Fatalf("manifest switch = %v; want a stored false", man.ReviewsDisabled)
	}
	if k.srv.ReviewsDisabled() || k.srv.ReviewsPaused() {
		t.Fatalf("running project = disabled %v paused %v; want both open",
			k.srv.ReviewsDisabled(), k.srv.ReviewsPaused())
	}
}

// null clears the override, so the project follows the instance default again —
// and the running switch takes that default now, not at the next attach.
func TestProjectReviewSwitchClearFollowsInstanceDefault(t *testing.T) {
	f := newInstanceReviewsHub(t, false)
	putInstanceRuntime(t, f.h, `{"reviews_disabled":true}`) // instance default closed
	p, k := f.attached(t, "Acme", "acme.example")
	if !k.srv.ReviewsDisabled() {
		t.Fatal("fixture: the project should have inherited the closed instance default")
	}

	if code, out := putProject(t, f.h, p.ID, `{"reviews_disabled":false}`); code != http.StatusOK {
		t.Fatalf("override open = %d %v", code, out)
	}
	if k.srv.ReviewsDisabled() {
		t.Fatal("the open override did not reach the running project")
	}

	code, out := putProject(t, f.h, p.ID, `{"reviews_disabled":null}`)
	if code != http.StatusOK {
		t.Fatalf("clear = %d %v", code, out)
	}
	if out["reviews_disabled_override"] != nil {
		t.Fatalf("after clearing, override = %v; want null", out["reviews_disabled_override"])
	}
	if out["reviews_disabled"] != true {
		t.Fatalf("after clearing, effective = %v; want the closed instance default", out["reviews_disabled"])
	}
	if man := readManifest(t, p); man.ReviewsDisabled != nil {
		t.Fatal("clearing left the override in the manifest")
	}
	if !k.srv.ReviewsDisabled() {
		t.Fatal("clearing the override did not move the running project to the instance default")
	}

	// The instance default is an attach-time value: changing it later must not
	// flip a project that is already running.
	putInstanceRuntime(t, f.h, `{"reviews_disabled":false}`)
	if !k.srv.ReviewsDisabled() {
		t.Fatal("changing the instance default silently flipped the running project")
	}
	if info := f.h.info(p); info.ReviewsDisabled != true || info.ReviewsDisabledOverride != nil {
		t.Fatalf("index = effective %v override %v; want the running closed switch, no override",
			info.ReviewsDisabled, info.ReviewsDisabledOverride)
	}
}

// The three request shapes are distinct: absent leaves the switch alone, null
// clears it, true/false stores it. A bad type is a 400 that changes nothing.
func TestProjectReviewSwitchBodyShapes(t *testing.T) {
	f := newInstanceReviewsHub(t, false)
	p, k := f.attached(t, "Acme", "acme.example")

	// Absent: another field is saved, the switch stays as it was.
	if code, out := putProject(t, f.h, p.ID, `{"max_runs":2}`); code != http.StatusOK {
		t.Fatalf("max_runs-only save = %d %v", code, out)
	}
	if man := readManifest(t, p); man.ReviewsDisabled != nil {
		t.Fatal("a save that did not mention reviews wrote a switch")
	}
	if k.srv.ReviewsDisabled() {
		t.Fatal("a save that did not mention reviews closed them")
	}

	if code, out := putProject(t, f.h, p.ID, `{"reviews_disabled":true}`); code != http.StatusOK {
		t.Fatalf("store true = %d %v", code, out)
	}
	// Absent again: the stored value must survive an unrelated save.
	if code, out := putProject(t, f.h, p.ID, `{"max_runs":3}`); code != http.StatusOK {
		t.Fatalf("second max_runs-only save = %d %v", code, out)
	}
	if man := readManifest(t, p); man.ReviewsDisabled == nil || !*man.ReviewsDisabled {
		t.Fatalf("an unrelated save dropped the stored switch: %v", man.ReviewsDisabled)
	}
	if !k.srv.ReviewsDisabled() {
		t.Fatal("an unrelated save reopened the running project")
	}

	if code, out := putProject(t, f.h, p.ID, `{"reviews_disabled":null}`); code != http.StatusOK {
		t.Fatalf("clear = %d %v", code, out)
	}
	if man := readManifest(t, p); man.ReviewsDisabled != nil {
		t.Fatal("null did not clear the stored switch")
	}
	// null on a project that already follows the instance is a no-op, not an
	// error: the console sends it for the "恢复跟随实例" button.
	if code, out := putProject(t, f.h, p.ID, `{"reviews_disabled":null}`); code != http.StatusOK {
		t.Fatalf("clear without an override = %d %v", code, out)
	}

	for _, body := range []string{
		`{"reviews_disabled":"yes"}`,
		`{"reviews_disabled":1}`,
		`{"reviews_disabled":{}}`,
	} {
		code, out := putProject(t, f.h, p.ID, body)
		if code != http.StatusBadRequest {
			t.Fatalf("%s = %d %v; want 400", body, code, out)
		}
	}
	if man := readManifest(t, p); man.ReviewsDisabled != nil {
		t.Fatal("a rejected request wrote a switch")
	}
	if k.srv.ReviewsDisabled() {
		t.Fatal("a rejected request closed the running switch")
	}
}

func putProject(t *testing.T, h *hub, id, body string) (int, map[string]any) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/projects/"+id, strings.NewReader(body))
	h.updateProject(rec, req, id)
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("response is not JSON: %v (%s)", err, rec.Body.String())
	}
	return rec.Code, out
}
