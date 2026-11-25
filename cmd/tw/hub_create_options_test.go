package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kksty/tianwu/internal/board"
	"github.com/kksty/tianwu/internal/decider"
	"github.com/kksty/tianwu/internal/executor"
	"github.com/kksty/tianwu/internal/node"
	"github.com/kksty/tianwu/internal/project"
	"github.com/kksty/tianwu/internal/vulnlib"
)

// A new project can be created with the options that used to require create →
// pause → settings → resume. The assertions below are on the persisted manifest
// and on the values attach actually reads, not on the response alone: a response
// echoing the request would pass while the first round still ran on the instance
// defaults.
func TestCreateProjectOptionsApplyBeforeAttach(t *testing.T) {
	h, mgr := createOptionsHub(t, hubConfig{MaxRuns: 3, RoundTimeout: 15 * time.Minute, NoDecider: true})
	code, out := postProject(t, h, `{"name":"Acme","scope":"acme.example","goal":"拿到边界主机",
		"reviews_disabled":true,"max_runs":2,"round_timeout":"10m"}`)
	if code != http.StatusCreated {
		t.Fatalf("create = %d %v", code, out)
	}
	if out["reviews_disabled"] != true || out["max_runs"] != float64(2) ||
		out["max_runs_effective"] != float64(2) || out["round_timeout"] != "10m" {
		t.Fatalf("created project reports %v; want reviews off / 2 / 10m", out)
	}

	id, _ := out["id"].(string)
	p := mgr.Get(id)
	if p == nil {
		t.Fatalf("project %q is not in the manager", id)
	}
	man := readManifest(t, p)
	if man.ReviewsDisabled == nil || !*man.ReviewsDisabled {
		t.Fatalf("manifest reviews_disabled = %v; want a stored true", man.ReviewsDisabled)
	}
	if man.MaxRuns == nil || *man.MaxRuns != 2 {
		t.Fatalf("manifest max_runs = %v; want 2", man.MaxRuns)
	}
	if man.RoundTimeout != "10m" {
		t.Fatalf("manifest round_timeout = %q; want 10m", man.RoundTimeout)
	}

	k := h.kits[id]
	if k == nil {
		t.Fatal("create did not attach the project")
	}
	if !k.srv.ReviewsDisabled() || !k.srv.ReviewsPaused() {
		t.Fatalf("attached switch = disabled %v paused %v; want both closed",
			k.srv.ReviewsDisabled(), k.srv.ReviewsPaused())
	}
	// attach hands the decider this exact duration (hub.go: r.RoundTimeout =
	// h.commanderRoundTimeout(p)); with --no-decider there is no runner to read,
	// so the call it would make is the observation.
	if got := h.commanderRoundTimeout(p); got != 10*time.Minute {
		t.Fatalf("commander round timeout = %s; want 10m", got)
	}

	// The first commander round's prompt is built from the attached switch. The
	// reviews-off identity must be the variant actually selected, and the same
	// builder with the switch open must not contain it (the marker discriminates).
	prompt := decider.CommanderRoundPrompt(board.View{}, "笔记", "", decider.ContextStatus{},
		decider.RoundContext{ReviewsOff: k.srv.ReviewsDisabled()})
	if !strings.Contains(prompt, "复核已关闭。必要前提的准入结果与阻塞原因见 readiness。") {
		t.Fatal("first-round prompt kept the reviews-open rules although the project was created with reviews off")
	}
	open := decider.CommanderRoundPrompt(board.View{}, "笔记", "", decider.ContextStatus{},
		decider.RoundContext{ReviewsOff: false})
	if strings.Contains(open, "复核已关闭。必要前提的准入结果与阻塞原因见 readiness。") {
		t.Fatal("control: the reviews-off marker appears in the reviews-open prompt, so the assertion above proves nothing")
	}
}

// The persisted value wins over the instance boot flag in both directions: a
// project created with reviews explicitly open runs open under --no-verify.
func TestCreateProjectReviewsOpenOverridesBootFlag(t *testing.T) {
	h, mgr := createOptionsHub(t, hubConfig{NoVerify: true, NoDecider: true})
	code, out := postProject(t, h, `{"name":"Acme","scope":"acme.example","reviews_disabled":false}`)
	if code != http.StatusCreated {
		t.Fatalf("create = %d %v", code, out)
	}
	p := mgr.Get(out["id"].(string))
	man := readManifest(t, p)
	if man.ReviewsDisabled == nil || *man.ReviewsDisabled {
		t.Fatalf("manifest reviews_disabled = %v; want a stored false", man.ReviewsDisabled)
	}
	k := h.kits[p.ID]
	if k == nil {
		t.Fatal("create did not attach the project")
	}
	if k.srv.ReviewsDisabled() {
		t.Fatal("a project created with reviews open still attached with them closed")
	}
}

// "Round timeout 0" is a deliberate unlimited, not "inherit": it must survive
// creation as a stored zero rather than being dropped as an empty value.
func TestCreateProjectRoundTimeoutZeroIsUnlimited(t *testing.T) {
	h, mgr := createOptionsHub(t, hubConfig{RoundTimeout: 15 * time.Minute, NoDecider: true})
	code, out := postProject(t, h, `{"name":"Acme","scope":"acme.example","round_timeout":"0"}`)
	if code != http.StatusCreated {
		t.Fatalf("create = %d %v", code, out)
	}
	p := mgr.Get(out["id"].(string))
	if got := readManifest(t, p).RoundTimeout; got != "0" {
		t.Fatalf("manifest round_timeout = %q; want \"0\"", got)
	}
	if got := h.commanderRoundTimeout(p); got != 0 {
		t.Fatalf("commander round timeout = %s; want unlimited", got)
	}
}

// Omitting the options keeps the pre-options behaviour: the manifest a console
// gets is byte for byte the one Manager.Create writes, and the runtime follows
// the instance flags.
func TestCreateProjectWithoutOptionsIsUnchanged(t *testing.T) {
	h, mgr := createOptionsHub(t, hubConfig{MaxRuns: 3, RoundTimeout: 15 * time.Minute, NoVerify: true, NoDecider: true})
	baseline, err := mgr.Create("Acme", "acme.example")
	if err != nil {
		t.Fatal(err)
	}
	code, out := postProject(t, h, `{"name":"Acme","scope":"acme.example"}`)
	if code != http.StatusCreated {
		t.Fatalf("create = %d %v", code, out)
	}
	created := mgr.Get(out["id"].(string))
	if created == nil {
		t.Fatalf("project %v is not in the manager", out["id"])
	}
	if a, b := manifestBytes(t, baseline), manifestBytes(t, created); a != b {
		t.Fatalf("manifest differs from a plain create:\n baseline=%s\n created =%s", a, b)
	}
	for _, key := range []string{`"reviews_disabled"`, `"max_runs"`, `"round_timeout"`} {
		if strings.Contains(manifestBytes(t, created), key) {
			t.Fatalf("an omitted option was written as %s; inherit must stay absent", key)
		}
	}
	if out["max_runs"] != nil || out["max_runs_effective"] != float64(3) {
		t.Fatalf("no override reports %v; want null / instance 3", out)
	}
	k := h.kits[created.ID]
	if k == nil {
		t.Fatal("create did not attach the project")
	}
	if !k.srv.ReviewsDisabled() {
		t.Fatal("no stored switch, --no-verify boot: reviews must attach closed")
	}
}

// A rejected option is refused before anything is written: no project
// directory, no runtime, no half-configured engagement to clean up.
func TestCreateProjectRejectsBadOptionsWithoutCreating(t *testing.T) {
	h, mgr := createOptionsHub(t, hubConfig{MaxRuns: 3, NoDecider: true})
	before := len(mgr.All())
	for _, c := range []struct{ name, body, want string }{
		{"round_timeout not a duration", `{"scope":"acme.example","round_timeout":"nope"}`, "round_timeout"},
		{"round_timeout negative", `{"scope":"acme.example","round_timeout":"-5m"}`, "round_timeout"},
		{"max_runs negative", `{"scope":"acme.example","max_runs":-1}`, "max_runs"},
		{"max_runs not an integer", `{"scope":"acme.example","max_runs":"two"}`, "max_runs"},
	} {
		code, out := postProject(t, h, c.body)
		if code != http.StatusBadRequest {
			t.Fatalf("%s: create = %d %v; want 400", c.name, code, out)
		}
		msg, _ := out["error"].(string)
		if !strings.Contains(msg, c.want) {
			t.Fatalf("%s: error %q does not name %s", c.name, msg, c.want)
		}
		if got := len(mgr.All()); got != before {
			t.Fatalf("%s: projects went %d → %d; a rejected option left one behind", c.name, before, got)
		}
		if len(h.kits) != 0 {
			t.Fatalf("%s: a runtime was attached for a refused project", c.name)
		}
	}
	// Positive control: the same endpoint accepts a valid request, so the 400s
	// above are the validation and not a handler that always refuses.
	if code, out := postProject(t, h, `{"scope":"acme.example","round_timeout":"10m","max_runs":1}`); code != http.StatusCreated {
		t.Fatalf("valid create = %d %v", code, out)
	}
}

func createOptionsHub(t *testing.T, cfg hubConfig) (*hub, *project.Manager) {
	t.Helper()
	// Best-effort removal: attach starts watchers that outlive the assertions,
	// and t.TempDir's fatal cleanup would turn that into "directory not empty".
	dir, err := os.MkdirTemp("", "tw-create-options-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	mgr, err := project.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	lib, err := vulnlib.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Nodes == nil {
		cfg.Nodes = &node.Registry{}
	}
	return &hub{
		cfg:           cfg,
		mgr:           mgr,
		ctx:           ctx,
		vulns:         lib,
		kits:          map[string]*kit{},
		auxiliaryGate: executor.NewGate(1),
	}, mgr
}

func postProject(t *testing.T, h *hub, body string) (int, map[string]any) {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/api/projects", strings.NewReader(body))
	w := httptest.NewRecorder()
	h.serveProjects(w, r)
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("response is not JSON: %v (%s)", err, w.Body.String())
	}
	return w.Code, out
}

func readManifest(t *testing.T, p *project.Project) project.Manifest {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(p.Dir, project.ManifestFile))
	if err != nil {
		t.Fatal(err)
	}
	var m project.Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("manifest is not JSON: %v", err)
	}
	return m
}

// manifestBytes is the manifest as content: the identity fields (random id and
// creation time) are the only ones two projects may legitimately differ in, so
// zeroing them makes a byte comparison of the rest meaningful.
func manifestBytes(t *testing.T, p *project.Project) string {
	t.Helper()
	m := readManifest(t, p)
	m.ID = ""
	m.Created = time.Time{}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
