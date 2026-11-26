package project

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/kksty/saker/internal/model"
	"github.com/kksty/saker/internal/store"
)

// The scope is a human snapshot of the current boundary, not a whitelist: the
// engagement keeps working when the operator writes prose or when assets change
// mid-test. Nothing may parse it, validate it as URLs/IPs, or reject it — a
// programmatic check here would forbid exactly the dynamic additions the
// operator makes through messages.
func TestScopeIsFreeTextAndNeverValidated(t *testing.T) {
	root := t.TempDir()
	m, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	scope := "目标主站及同单位相关资产（子域/IP 以现场发现为准）\n" +
		"过程中新增的站点以操作员下发为准；不做破坏性操作\n" +
		"not-a-url / 10.0.0.0/24 / 任意中文"
	p, err := m.Create("free scope", scope)
	if err != nil {
		t.Fatalf("free-text scope was rejected: %v", err)
	}
	if p.Scope != scope {
		t.Fatalf("scope was normalized: %q", p.Scope)
	}
	if err := m.SetScope(p.ID, "改成只测 a.example 和 b.example"); err != nil {
		t.Fatalf("updating the boundary was rejected: %v", err)
	}
	if m.Get(p.ID).Scope != "改成只测 a.example 和 b.example" {
		t.Fatal("scope update was not stored verbatim")
	}
}

func TestProjectPauseSurvivesReopenUntilExplicitResume(t *testing.T) {
	root := t.TempDir()
	m, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	p, err := m.Create("paused", "scope")
	if err != nil {
		t.Fatal(err)
	}
	if err = p.SetPaused(true); err != nil {
		t.Fatal(err)
	}
	m, err = Open(root)
	if err != nil {
		t.Fatal(err)
	}
	p = m.Get(p.ID)
	if !p.IsPaused() {
		t.Fatal("restart silently resumed project")
	}
	if err = p.SetPaused(false); err != nil {
		t.Fatal(err)
	}
	m, err = Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if m.Get(p.ID).IsPaused() {
		t.Fatal("explicit resume was not persisted")
	}
}

// The pause cause is persisted state too: after a restart the console still has
// to read "commander requested stop" instead of falling back to the manual-pause
// default (the runtime classifies from a machine code, never from the reason
// prose). A plain pause is a transition and keeps the cause; resume clears it;
// an explicit cause replaces it.
func TestPauseKindPersistsAcrossRestartAndPlainPause(t *testing.T) {
	root := t.TempDir()
	m, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	p, err := m.Create("commander stop", "scope")
	if err != nil {
		t.Fatal(err)
	}
	if k := p.PauseKindOf(); k != "" {
		t.Fatalf("a new project has pause kind %q, want empty", k)
	}
	if err := p.SetPausedWithKind(true, "commander_standby"); err != nil {
		t.Fatal(err)
	}

	// Reopen from disk: the machine kind survives with the paused flag.
	m, err = Open(root)
	if err != nil {
		t.Fatal(err)
	}
	p = m.Get(p.ID)
	if !p.IsPaused() || p.PauseKindOf() != "commander_standby" {
		t.Fatalf("reopened pause = %v kind %q, want paused commander_standby", p.IsPaused(), p.PauseKindOf())
	}
	// A plain pause may not silently reclassify the cause (boot/re-attach paths
	// call SetPaused(true) without knowing why the project was parked).
	if err := p.SetPaused(true); err != nil {
		t.Fatal(err)
	}
	m, err = Open(root)
	if err != nil {
		t.Fatal(err)
	}
	p = m.Get(p.ID)
	if k := p.PauseKindOf(); k != "commander_standby" {
		t.Fatalf("plain pause rewrote pause kind to %q", k)
	}

	// Resume clears it; a later pause without a cause leaves it empty.
	if err := p.SetPaused(false); err != nil {
		t.Fatal(err)
	}
	m, err = Open(root)
	if err != nil {
		t.Fatal(err)
	}
	p = m.Get(p.ID)
	if p.IsPaused() || p.PauseKindOf() != "" {
		t.Fatalf("resume left paused=%v kind=%q", p.IsPaused(), p.PauseKindOf())
	}
	if err := p.SetPausedWithKind(true, "completion_standby"); err != nil {
		t.Fatal(err)
	}
	m, err = Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if k := m.Get(p.ID).PauseKindOf(); k != "completion_standby" {
		t.Fatalf("explicit cause after reopen = %q, want completion_standby", k)
	}
}

// The whole point of the project layer: two engagements share a process but not
// a ledger, so a fact declared in one is invisible in the other.
// The directory is an opaque address, not a summary of the engagement: a name
// containing a target host or URL must not leak into the path or console URL,
// and two same-named projects must still get distinct directories.
func TestProjectDirectoryIDIsOpaqueAndIndependentOfName(t *testing.T) {
	m, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	name := "61.133.196.36 与 http://www.nx.example/ 门户外网评估"
	a, err := m.Create(name, "scope")
	if err != nil {
		t.Fatal(err)
	}
	if a.Name != name {
		t.Fatalf("display name = %q, want the operator's name verbatim", a.Name)
	}
	// The id is a random token, not a name-derived slug. Assert the shape, not
	// the absence of name fragments: a short random hex token can contain "61"
	// or "133" by chance, so substring checks are not a property of an opaque id
	// (and made this test fail in roughly one run out of twenty).
	if !regexp.MustCompile(`^p-[a-z2-7]{6}$`).MatchString(a.ID) {
		t.Fatalf("directory id %q is not an opaque p-xxxxxx token", a.ID)
	}
	b, err := m.Create("same name", "scope")
	if err != nil {
		t.Fatal(err)
	}
	c, err := m.Create("same name", "scope")
	if err != nil {
		t.Fatal(err)
	}
	if b.ID == c.ID || b.Dir == c.Dir {
		t.Fatalf("same-named projects share an address: %q / %q", b.ID, c.ID)
	}
}

func TestCreateKeepsLedgersApart(t *testing.T) {
	root := t.TempDir()
	m, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	a, err := m.Create("Acme Web", "acme.example")
	if err != nil {
		t.Fatal(err)
	}
	b, err := m.Create("Beta", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(a.ID) != 8 || !strings.HasPrefix(a.ID, "p-") {
		t.Fatalf("id = %q, want the short opaque p-xxxxxx directory id", a.ID)
	}
	if strings.Contains(a.ID, "acme") {
		t.Fatalf("id = %q, want the name kept out of the directory address", a.ID)
	}
	if _, err := a.Store.AddFact(model.Fact{Title: "only in acme", Kind: "vuln"}); err != nil {
		t.Fatal(err)
	}
	if got := len(b.Store.Snapshot().Facts); got != 0 {
		t.Fatalf("beta sees %d facts, want 0", got)
	}
	if a.CaseDir == b.CaseDir {
		t.Fatalf("projects share a workspace: %s", a.CaseDir)
	}
	if _, err := os.Stat(filepath.Join(a.Dir, ManifestFile)); err != nil {
		t.Fatalf("manifest not written: %v", err)
	}
	if _, err := os.Stat(a.CaseDir); err != nil {
		t.Fatalf("workspace not created: %v", err)
	}

	// A restart must find the same projects, names and facts.
	m2, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(m2.All()) != 2 {
		t.Fatalf("reopened %d projects, want 2", len(m2.All()))
	}
	again := m2.Get(a.ID)
	if again == nil {
		t.Fatalf("project %s lost on reopen", a.ID)
	}
	if again.Name != "Acme Web" || again.Scope != "acme.example" {
		t.Fatalf("manifest not restored: %+v", again.Manifest)
	}
	if got := len(again.Store.Snapshot().Facts); got != 1 {
		t.Fatalf("facts after reopen = %d, want 1", got)
	}
}

// The console's per-project concurrency cap lives in the manifest: an override
// survives a restart, and clearing it restores "inherit the instance default"
// instead of quietly writing 0 (which means uncapped).
func TestMaxRunsOverridePersists(t *testing.T) {
	root := t.TempDir()
	m, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	p, err := m.Create("Acme", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := p.MaxRunsOverride(); ok {
		t.Fatal("a new project has an override, want inherit")
	}
	two := 2
	if err := m.SetMaxRuns(p.ID, &two); err != nil {
		t.Fatal(err)
	}

	m2, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	again := m2.Get(p.ID)
	if v, ok := again.MaxRunsOverride(); !ok || v != 2 {
		t.Fatalf("override after reopen = %d, %v; want 2, true", v, ok)
	}

	if err := m2.SetMaxRuns(p.ID, nil); err != nil {
		t.Fatal(err)
	}
	m3, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := m3.Get(p.ID).MaxRunsOverride(); ok {
		t.Fatal("cleared override survived the restart")
	}
}

// verify_auto is retired. A manifest that still carries it must keep loading —
// no error, no migration — and the value must survive an unrelated save
// untouched, so an old engagement is never silently rewritten. Nothing reads it:
// the review policy comes from the premise policy alone.
func TestLegacyVerifyAutoManifestIsParsedAndIgnored(t *testing.T) {
	root := t.TempDir()
	m, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	p, err := m.Create("Acme", "")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(p.Dir, ManifestFile)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var manifest map[string]any
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	manifest["verify_auto"] = true
	edited, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, edited, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := p.Store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(root)
	if err != nil {
		t.Fatalf("a manifest with the retired verify_auto must still load: %v", err)
	}
	got := reopened.Get(p.ID)
	if got == nil {
		t.Fatal("project disappeared after reload")
	}
	if got.PremisePolicyView() != PremisePolicyVerifiedOnly {
		t.Fatalf("retired verify_auto changed the review policy: %q", got.PremisePolicyView())
	}

	// An unrelated save rewrites the manifest. It must carry the legacy value
	// through verbatim instead of dropping or reinterpreting it.
	if err := reopened.SetScope(p.ID, "still here"); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var saved map[string]any
	if err := json.Unmarshal(after, &saved); err != nil {
		t.Fatal(err)
	}
	if saved["verify_auto"] != true {
		t.Fatalf("legacy verify_auto was rewritten by an unrelated save: %v", saved["verify_auto"])
	}
}

func TestCreateDedupesIDs(t *testing.T) {
	m, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a, err := m.Create("dup", "")
	if err != nil {
		t.Fatal(err)
	}
	b, err := m.Create("dup", "")
	if err != nil {
		t.Fatal(err)
	}
	if a.ID == b.ID {
		t.Fatalf("two projects got one id: %s", a.ID)
	}
	if a.Dir == b.Dir {
		t.Fatalf("two projects share a directory: %s", a.Dir)
	}
}

func TestOpenAdoptsDirectoryWithoutManifest(t *testing.T) {
	root := t.TempDir()
	d := filepath.Join(root, "restored")
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(d, "ledger.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddFact(model.Fact{Title: "restored fact"}); err != nil {
		t.Fatal(err)
	}

	m, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	p := m.Get("restored")
	if p == nil {
		t.Fatal("directory without a manifest was not adopted")
	}
	if p.Name != "restored" {
		t.Fatalf("name = %q, want the directory name", p.Name)
	}
	if got := len(p.Store.Snapshot().Facts); got != 1 {
		t.Fatalf("facts = %d, want 1", got)
	}
	if _, err := os.Stat(filepath.Join(d, ManifestFile)); err != nil {
		t.Fatalf("adopted project has no manifest: %v", err)
	}
}

// A pre-project data directory is moved into projects/default once, workspaces
// included, and the second call is a no-op.

// A directory carrying only the retired JSON ledger is not migrated and not
// replaced. It is skipped and reported, and every byte of it is left alone: the
// instance starts for the projects that do load.
func TestOpenSkipsProjectWithoutSQLiteWithoutCreatingEmptyLedger(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "old")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	original := []byte(`{"version":10,"facts":[]}`)
	path := filepath.Join(dir, "ledger.json")
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	mgr, err := Open(root)
	if err != nil {
		t.Fatalf("a project without a SQLite ledger must not stop the instance: %v", err)
	}
	if mgr.Len() != 0 {
		t.Fatalf("loaded = %d, want 0: the old project has no SQLite ledger", mgr.Len())
	}
	if skipped := mgr.Skipped(); len(skipped) != 1 || skipped[0].ID != "old" {
		t.Fatalf("skipped = %+v, want the old directory", skipped)
	}
	if _, err := os.Stat(filepath.Join(dir, "ledger.sqlite")); !os.IsNotExist(err) {
		t.Fatal("empty replacement created")
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != string(original) {
		t.Fatal("old project changed")
	}
}
