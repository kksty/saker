package main

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/kksty/saker/internal/executor"
	"github.com/kksty/saker/internal/node"
	"github.com/kksty/saker/internal/project"
	"github.com/kksty/saker/internal/settings"
	"github.com/kksty/saker/internal/vulnlib"
)

// The review switch has two levels: an instance default (saved in the instance
// settings, initialised by --no-verify) and a project override (the project
// manifest). These tests pin which one wins where, because the two used to be one
// flag and an operator could not tell them apart.

type instanceReviewsFixture struct {
	h    *hub
	mgr  *project.Manager
	st   *settings.Store
	path string
}

func newInstanceReviewsHub(t *testing.T, boot bool) *instanceReviewsFixture {
	t.Helper()
	// Best-effort removal: attach starts watchers that outlive the assertions.
	root, err := os.MkdirTemp("", "saker-instance-reviews-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	mgr, err := project.Open(filepath.Join(root, "projects"))
	if err != nil {
		t.Fatal(err)
	}
	legacy := filepath.Join(root, "settings.json")
	st, err := settings.LoadAt(legacy, "")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	lib, err := vulnlib.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return &instanceReviewsFixture{
		h: &hub{
			cfg:           hubConfig{Nodes: &node.Registry{}, NoVerify: boot, NoDecider: true},
			mgr:           mgr,
			settings:      st,
			ctx:           ctx,
			vulns:         lib,
			kits:          map[string]*kit{},
			auxiliaryGate: executor.NewGate(1),
		},
		mgr:  mgr,
		st:   st,
		path: legacy,
	}
}

type instanceRuntimeView struct {
	AuxiliaryConcurrency    int  `json:"auxiliary_concurrency"`
	MaxAuxiliaryConcurrency int  `json:"max_auxiliary_concurrency"`
	ReviewsDisabled         bool `json:"reviews_disabled"`
	ReviewsDisabledStored   bool `json:"reviews_disabled_stored"`
}

func getInstanceRuntime(t *testing.T, h *hub) instanceRuntimeView {
	t.Helper()
	rec := callRuntimeSettings(t, h, http.MethodGet, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/runtime-settings = %d %s", rec.Code, rec.Body.String())
	}
	var out instanceRuntimeView
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("GET body is not JSON: %v\n%s", err, rec.Body.String())
	}
	return out
}

func putInstanceRuntime(t *testing.T, h *hub, body string) instanceRuntimeView {
	t.Helper()
	rec := callRuntimeSettings(t, h, http.MethodPut, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT %s = %d %s", body, rec.Code, rec.Body.String())
	}
	var out instanceRuntimeView
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("PUT body is not JSON: %v\n%s", err, rec.Body.String())
	}
	return out
}

func (f *instanceReviewsFixture) create(t *testing.T, name, brief string) *project.Project {
	t.Helper()
	p, err := f.mgr.Create(name, brief)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// attached creates a project and attaches it in one step, which is what the hub
// does for a new project.
func (f *instanceReviewsFixture) attached(t *testing.T, name, brief string) (*project.Project, *kit) {
	t.Helper()
	p := f.create(t, name, brief)
	f.h.attach(p)
	k := f.h.kits[p.ID]
	if k == nil {
		t.Fatalf("attach did not register a kit for %s", p.ID)
	}
	return p, k
}

// With no saved instance value the --no-verify boot flag is the default, and it
// shows up as "not stored" so the console does not imply a save nobody made.
func TestInstanceReviewDefaultFallsBackToBootFlag(t *testing.T) {
	for _, tc := range []struct {
		name string
		boot bool
		want bool
	}{
		{"boot closed", true, true},
		{"boot open", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newInstanceReviewsHub(t, tc.boot)
			p, k := f.attached(t, "Acme", "acme.example")
			if got := k.srv.ReviewsDisabled(); got != tc.want {
				t.Fatalf("attached switch = %v; want the boot value %v", got, tc.want)
			}
			if info := f.h.info(p); info.ReviewsDisabled != tc.want {
				t.Fatalf("project index says %v; want %v", info.ReviewsDisabled, tc.want)
			}
			view := getInstanceRuntime(t, f.h)
			if view.ReviewsDisabled != tc.want || view.ReviewsDisabledStored {
				t.Fatalf("runtime settings = %+v; want boot value %v, not stored", view, tc.want)
			}
		})
	}
}

// A saved instance value beats the boot flag, in both directions: that is what
// makes the CLI flag an initial value instead of a second, hidden switch.
func TestStoredInstanceReviewDefaultBeatsBootFlag(t *testing.T) {
	f := newInstanceReviewsHub(t, true) // --no-verify: boot is closed
	view := putInstanceRuntime(t, f.h, `{"reviews_disabled":false}`)
	if view.ReviewsDisabled || !view.ReviewsDisabledStored {
		t.Fatalf("after saving open: %+v; want open and stored", view)
	}
	_, k := f.attached(t, "Acme", "acme.example")
	if k.srv.ReviewsDisabled() {
		t.Fatal("the saved open default lost to the --no-verify boot flag")
	}
	reloaded, err := settings.LoadAt(f.path, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := reloaded.RuntimeSettings().ReviewsDefaultDisabled(true); got {
		t.Fatal("the saved value did not reach the settings file")
	}
}

// A project's own switch wins over the instance default in both directions, and
// the index distinguishes "overridden" from "following" instead of reporting one
// boolean for both.
func TestProjectReviewOverrideWinsOverInstanceDefault(t *testing.T) {
	for _, tc := range []struct {
		name     string
		instance bool
		override bool
		want     bool
	}{
		{"instance closed, project open", true, false, false},
		{"instance open, project closed", false, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newInstanceReviewsHub(t, false)
			body := `{"reviews_disabled":false}`
			if tc.instance {
				body = `{"reviews_disabled":true}`
			}
			putInstanceRuntime(t, f.h, body)

			p, k := f.attached(t, "Acme", "acme.example")
			if err := p.SetReviewsDisabled(tc.override); err != nil {
				t.Fatal(err)
			}
			// Re-attach so the persisted override is what the runtime reads.
			k.srv.SetReviewsDisabled(!tc.override)
			f.h.attach(p)
			if got := f.h.kits[p.ID].srv.ReviewsDisabled(); got != tc.want {
				t.Fatalf("attached switch = %v; want the override %v", got, tc.want)
			}
			info := f.h.info(p)
			if info.ReviewsDisabled != tc.want {
				t.Fatalf("index effective = %v; want %v", info.ReviewsDisabled, tc.want)
			}
			if info.ReviewsDisabledOverride == nil || *info.ReviewsDisabledOverride != tc.override {
				t.Fatalf("index override = %v; want %v", info.ReviewsDisabledOverride, tc.override)
			}
		})
	}
}

// Changing the instance default is an attach-time change: a project created (or
// re-opened) afterwards follows it, a project with its own value does not, and a
// project that is already running is not flipped behind the operator's back —
// closing a live project's reviews cancels its review runs, and that stays the
// project page's own decision.
func TestChangingInstanceDefaultAffectsNewProjectsNotRunningOnes(t *testing.T) {
	f := newInstanceReviewsHub(t, false) // default open
	pRunning, runningKit := f.attached(t, "Running", "running.example")
	if runningKit.srv.ReviewsDisabled() {
		t.Fatal("fixture: the running project should have attached with reviews open")
	}

	putInstanceRuntime(t, f.h, `{"reviews_disabled":true}`)

	if _, k := f.attached(t, "New", "new.example"); !k.srv.ReviewsDisabled() {
		t.Fatal("a project attached after the default changed did not follow it")
	}
	if runningKit.srv.ReviewsDisabled() {
		t.Fatal("changing the instance default silently flipped a running project's switch")
	}
	// The index reports the running project's real switch, not the new default.
	if info := f.h.info(pRunning); info.ReviewsDisabled {
		t.Fatal("the index claims the running project's reviews are closed while they are open")
	}
	if info := f.h.info(pRunning); info.ReviewsDisabledOverride != nil {
		t.Fatalf("a project that never stored a switch reports an override: %v", *info.ReviewsDisabledOverride)
	}

	// An explicit override survives the default changing underneath it.
	pOverride, _ := f.attached(t, "Override", "override.example")
	if err := pOverride.SetReviewsDisabled(false); err != nil {
		t.Fatal(err)
	}
	putInstanceRuntime(t, f.h, `{"reviews_disabled":true}`)
	f.h.attach(pOverride)
	if f.h.kits[pOverride.ID].srv.ReviewsDisabled() {
		t.Fatal("an explicit open override lost to the closed instance default")
	}
}

// One endpoint edits both instance knobs, and an absent field keeps its stored
// value: a console that only moves the review switch must not reset the
// concurrency, and a null or empty body is refused rather than read as a default.
func TestRuntimeSettingsEditsReviewDefaultIndependently(t *testing.T) {
	f := newInstanceReviewsHub(t, false)
	view := getInstanceRuntime(t, f.h)
	if view.AuxiliaryConcurrency != 1 || view.ReviewsDisabled || view.ReviewsDisabledStored {
		t.Fatalf("fresh runtime settings = %+v", view)
	}

	if view = putInstanceRuntime(t, f.h, `{"reviews_disabled":true}`); view.AuxiliaryConcurrency != 1 || !view.ReviewsDisabled {
		t.Fatalf("review-only save = %+v; want concurrency untouched, reviews closed", view)
	}
	if view = putInstanceRuntime(t, f.h, `{"auxiliary_concurrency":2}`); view.AuxiliaryConcurrency != 2 || !view.ReviewsDisabled {
		t.Fatalf("concurrency-only save = %+v; want reviews still closed", view)
	}
	if view = putInstanceRuntime(t, f.h, `{"auxiliary_concurrency":3,"reviews_disabled":false}`); view.AuxiliaryConcurrency != 3 || view.ReviewsDisabled {
		t.Fatalf("both-fields save = %+v", view)
	}

	good := getInstanceRuntime(t, f.h)
	for _, body := range []string{
		`{}`,
		`{"reviews_disabled":null}`,
		`{"auxiliary_concurrency":null}`,
		`{"reviews_disabled":"yes"}`,
		`{"reviews_disabled":1}`,
		`{"auxiliary_concurrency":0}`,
		`{"auxiliary_concurrency":9}`,
		`{"bogus":true}`,
	} {
		rec := callRuntimeSettings(t, f.h, http.MethodPut, body)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("PUT %s = %d, want 400: %s", body, rec.Code, rec.Body.String())
		}
		if got := getInstanceRuntime(t, f.h); got != good {
			t.Fatalf("PUT %s changed the settings to %+v", body, got)
		}
	}

	reloaded, err := settings.LoadAt(f.path, "")
	if err != nil {
		t.Fatal(err)
	}
	rt := reloaded.RuntimeSettings()
	if rt.AuxiliaryConcurrency != 3 || rt.ReviewsDefaultDisabled(true) {
		t.Fatalf("persisted runtime = %+v; want concurrency 3 and reviews open", rt)
	}
}
