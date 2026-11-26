package bundle

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kksty/saker/internal/model"
	"github.com/kksty/saker/internal/store"
)

// seedProject builds a project directory with the durable files a real
// engagement has, plus the runtime files a transfer must not carry.
func seedProject(t *testing.T) (dir string, st *store.Store, proofDigest string) {
	t.Helper()
	dir = filepath.Join(t.TempDir(), "projects", "p-test")
	for _, sub := range []string{"cases/in_1/tmp", "cases/in_1/.tw", "cases/in_1/.verify", "cases/in_1/.native", "cases/.access/ac_1", "commander/.tw", "runs"} {
		if err := os.MkdirAll(filepath.Join(dir, filepath.FromSlash(sub)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	proof := []byte("HTTP/1.1 200 OK\nadmin=1\n")
	proofDigest = hex.EncodeToString(sha256Sum(proof))
	for path, body := range map[string][]byte{
		"project.json":                            []byte(`{"id":"p-test","name":"测试项目","scope":"example.test","paused":false,"premise_policy":"verified_only"}`),
		"state.md":                                []byte("## 已确认\n- 登录可用\n"),
		"decisions.jsonl":                         []byte(`{"at":"2026-01-01T00:00:00Z"}` + "\n"),
		"operator-actions.jsonl":                  []byte(`{"action":"goal_met","goal_id":"g_1","by":"operator"}` + "\n"),
		"runs/r_1.jsonl":                          []byte(`{"event":"start"}` + "\n"),
		"cases/in_1/tmp/admin.txt":                proof,
		"cases/in_1/notes.md":                     []byte("direction notes"),
		"cases/in_1/.tw/worker-note.md":           []byte("跨轮记忆：下一步读后台"),
		"cases/in_1/.tw/facts.jsonl":              []byte("{\"kind\":\"note\",\"declaration_id\":\"fixture\",\"title\":\"fixture\"}\n"),
		"cases/in_1/.tw/facts.offset":             []byte("0"),
		"cases/in_1/.tw/auth":                     []byte("Bearer stale-credential"),
		"cases/in_1/.tw/aop-session.json":         []byte(`{"url":"http://old","session":"s"}`),
		"cases/in_1/.verify/leftover.md":          []byte("review scratch"),
		"cases/in_1/.native/result.json":          []byte(`{"tool":"curl"}`),
		"cases/.access/ac_1/entry.json":           []byte(`{"id":"ac_1","kind":"request"}`),
		"cases/.access/ac_1/material.json":        []byte(`{"method":"GET"}`),
		"cases/.access/ac_1/control-session.json": []byte(`{"live":true}`),
		"commander/.tw/auth":                      []byte("Bearer commander"),
		"commander/scratch.json":                  []byte(`{}`),
	} {
		p := filepath.Join(dir, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	st, err := store.OpenSQLite(filepath.Join(dir, "ledger.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if _, err := st.AddFact(model.Fact{Title: "后台登录可用", Doc: "admin=1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddEvidence(model.Evidence{URI: "in_1/tmp/admin.txt", Digest: proofDigest, Size: int64(len(proof))}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddEvidence(model.Evidence{URI: "in_1/tmp/already-gone.txt"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddRun(model.Run{Node: "local"}); err != nil {
		t.Fatal(err)
	}
	return dir, st, proofDigest
}

func sha256Sum(b []byte) []byte {
	sum := sha256.Sum256(b)
	return sum[:]
}

func TestProjectExportImportRoundTrip(t *testing.T) {
	dir, st, _ := seedProject(t)
	archive := filepath.Join(t.TempDir(), "p-test.sakerproj")
	m, err := ExportProject(dir, st, archive, "test")
	if err != nil {
		t.Fatalf("ExportProject: %v", err)
	}
	if m.Format != ProjectFormat || m.Version != ProjectVersion {
		t.Fatalf("manifest = %+v", m)
	}
	if m.Project.ID != "p-test" || m.Project.Name != "测试项目" {
		t.Fatalf("project ref = %+v", m.Project)
	}
	if m.Counts.Facts != 1 || m.Counts.Evidence != 2 || m.Counts.Runs != 1 {
		t.Fatalf("counts = %+v", m.Counts)
	}
	if len(m.Missing) != 1 || m.Missing[0] != "in_1/tmp/already-gone.txt" {
		t.Fatalf("missing = %v", m.Missing)
	}

	found := map[string]bool{}
	for _, f := range m.Files {
		found[f.Path] = true
		if strings.Contains(f.Path, ".tw/auth") || strings.Contains(f.Path, "aop-session") ||
			strings.Contains(f.Path, "control-session") || strings.Contains(f.Path, ".verify/") ||
			strings.HasPrefix(f.Path, "commander/") {
			t.Fatalf("runtime or credential file exported: %s", f.Path)
		}
	}
	for _, want := range []string{
		"project.json", "ledger.sqlite", "state.md", "decisions.jsonl", "operator-actions.jsonl", "runs/r_1.jsonl",
		"cases/in_1/tmp/admin.txt", "cases/in_1/.tw/worker-note.md", "cases/in_1/.tw/facts.jsonl", "cases/in_1/.tw/facts.offset",
		"cases/in_1/.native/result.json", "cases/.access/ac_1/entry.json", "cases/.access/ac_1/material.json",
	} {
		if !found[want] {
			t.Fatalf("durable file missing from archive: %s", want)
		}
	}

	targetData := t.TempDir()
	got, err := ImportProject(archive, targetData)
	if err != nil {
		t.Fatalf("ImportProject: %v", err)
	}
	if got.Project.ID != "p-test" {
		t.Fatalf("imported id = %s", got.Project.ID)
	}
	target := filepath.Join(targetData, "projects", "p-test")
	for _, want := range []string{
		"project.json", "ledger.sqlite", "state.md", "decisions.jsonl", "operator-actions.jsonl", "runs/r_1.jsonl",
		"cases/in_1/tmp/admin.txt", "cases/in_1/.tw/worker-note.md", "cases/in_1/.tw/facts.jsonl", "cases/in_1/.tw/facts.offset",
	} {
		if _, err := os.Stat(filepath.Join(target, filepath.FromSlash(want))); err != nil {
			t.Fatalf("imported file missing: %s: %v", want, err)
		}
	}
	for _, forbidden := range []string{"cases/in_1/.tw/auth", "cases/in_1/.tw/aop-session.json", "cases/.access/ac_1/control-session.json", "commander"} {
		if _, err := os.Stat(filepath.Join(target, filepath.FromSlash(forbidden))); !os.IsNotExist(err) {
			t.Fatalf("runtime file survived import: %s (%v)", forbidden, err)
		}
	}

	var doc map[string]any
	raw, err := os.ReadFile(filepath.Join(target, "project.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if doc["paused"] != true || doc["pause_kind"] != "manual_pause" {
		t.Fatalf("imported project must be paused: %s", raw)
	}

	// The ledger is readable from the receiver's copy and still names the proof.
	copyStore, err := store.OpenSQLite(filepath.Join(target, "ledger.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer copyStore.Close()
	snap := copyStore.Snapshot()
	if len(snap.Facts) != 1 || len(snap.Evidence) != 2 {
		t.Fatalf("imported ledger = %+v", snap)
	}
	if _, err := os.Stat(filepath.Join(target, "cases", "in_1", "tmp", "admin.txt")); err != nil {
		t.Fatalf("evidence bytes missing: %v", err)
	}
}

func TestProjectImportRefusesExistingProject(t *testing.T) {
	dir, st, _ := seedProject(t)
	archive := filepath.Join(t.TempDir(), "p-test.sakerproj")
	if _, err := ExportProject(dir, st, archive, "test"); err != nil {
		t.Fatal(err)
	}
	data := t.TempDir()
	if _, err := ImportProject(archive, data); err != nil {
		t.Fatal(err)
	}
	if _, err := ImportProject(archive, data); err == nil || !strings.Contains(err.Error(), "已经存在") {
		t.Fatalf("second import = %v, want conflict", err)
	}
}

func TestProjectImportRejectsTamperedArchive(t *testing.T) {
	dir, st, _ := seedProject(t)
	archive := filepath.Join(t.TempDir(), "p-test.sakerproj")
	if _, err := ExportProject(dir, st, archive, "test"); err != nil {
		t.Fatal(err)
	}
	corruptZipEntry(t, archive, "state.md", []byte("tampered"))
	data := t.TempDir()
	if _, err := ImportProject(archive, data); err == nil {
		t.Fatal("a tampered archive must be refused")
	}
	if _, err := os.Stat(filepath.Join(data, "projects", "p-test")); !os.IsNotExist(err) {
		t.Fatal("a refused import must leave no project directory")
	}
}

func TestProjectImportRejectsUnrecordedMissingEvidence(t *testing.T) {
	dir, st, _ := seedProject(t)
	archive := filepath.Join(t.TempDir(), "p-test.sakerproj")
	if _, err := ExportProject(dir, st, archive, "test"); err != nil {
		t.Fatal(err)
	}
	entries := readZipEntries(t, archive)
	byName := make(map[string][]byte, len(entries))
	var m ProjectManifest
	for _, e := range entries {
		byName[e.name] = e.body
		if e.name == ProjectManifestFile {
			if err := json.Unmarshal(e.body, &m); err != nil {
				t.Fatal(err)
			}
		}
	}
	m.Missing = nil
	byName[ProjectManifestFile], _ = json.Marshal(m)
	for i := range entries {
		entries[i].body = byName[entries[i].name]
	}
	writeZipEntries(t, archive, entries)
	data := t.TempDir()
	if _, err := ImportProject(archive, data); err == nil || !strings.Contains(err.Error(), "没有记录") {
		t.Fatalf("unrecorded gap = %v", err)
	}
}

func TestProjectImportRejectsEvidenceDigestConflict(t *testing.T) {
	dir, st, _ := seedProject(t)
	archive := filepath.Join(t.TempDir(), "p-test.sakerproj")
	if _, err := ExportProject(dir, st, archive, "test"); err != nil {
		t.Fatal(err)
	}
	// Rebuild the archive with different proof bytes and update every manifest
	// hash, so the zip's own integrity check passes. Only the ledger's evidence
	// digest can catch that these are not the admitted bytes: exactly the
	// "internally consistent but lying" archive import must refuse.
	entries := readZipEntries(t, archive)
	byName := make(map[string][]byte, len(entries))
	var m ProjectManifest
	for _, e := range entries {
		byName[e.name] = e.body
		if e.name == ProjectManifestFile {
			if err := json.Unmarshal(e.body, &m); err != nil {
				t.Fatal(err)
			}
		}
	}
	byName["cases/in_1/tmp/admin.txt"] = []byte("HTTP/1.1 200 OK\nadmin=0\n")
	for i := range m.Files {
		body := byName[m.Files[i].Path]
		sum := sha256.Sum256(body)
		m.Files[i].Size = int64(len(body))
		m.Files[i].SHA256 = hex.EncodeToString(sum[:])
	}
	byName[ProjectManifestFile], _ = json.Marshal(m)
	for i := range entries {
		entries[i].body = byName[entries[i].name]
	}
	writeZipEntries(t, archive, entries)
	data := t.TempDir()
	if _, err := ImportProject(archive, data); err == nil {
		t.Fatal("a bundle with an evidence digest conflict must be refused")
	}
}

func TestSafeNamesRejectWindowsDevicesAndTrailingDots(t *testing.T) {
	for _, id := range []string{"CON", "NUL", "com1", "LPT9", "p-x.", "p-x "} {
		if err := safeProjectID(id); err == nil {
			t.Fatalf("safeProjectID(%q) accepted a Windows-unsafe id", id)
		}
	}
	for _, name := range []string{"cases/CON", "cases/in_1/NUL.txt", "cases/in_1/name.", "cases/in_1/name "} {
		if _, err := safeName(name); err == nil {
			t.Fatalf("safeName(%q) accepted a Windows-unsafe entry", name)
		}
	}
	if _, err := safeName("cases/in_1/admin.txt"); err != nil {
		t.Fatalf("ordinary path rejected: %v", err)
	}
}

func TestCleanTransferRootRemovesCrashedStaging(t *testing.T) {
	data := t.TempDir()
	root := filepath.Join(data, ".transfer")
	first := filepath.Join(root, "project-first")
	second := filepath.Join(root, "project-second")
	other := filepath.Join(root, "notes")
	for _, dir := range []string{first, second, other} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := CleanTransferRoot(data); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{first, second} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("staging survived: %s: %v", path, err)
		}
	}
	// Only project-* staging is this helper's business.
	if _, err := os.Stat(other); err != nil {
		t.Fatalf("unrelated entry removed: %s: %v", other, err)
	}
}

func TestCleanTransferRootKeepsCompletedArchives(t *testing.T) {
	data := t.TempDir()
	exports := filepath.Join(data, "exports")
	if err := os.MkdirAll(exports, 0o700); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(exports, "upload-example-20261120-120000.sakerproj")
	if err := os.WriteFile(archive, []byte("export"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A process can crash while receiving the body, before creating an import
	// staging directory.
	root := filepath.Join(data, ".transfer")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	upload := filepath.Join(root, "upload-123.sakerproj")
	if err := os.WriteFile(upload, []byte("partial upload"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := CleanTransferRoot(data); err != nil {
		t.Fatal(err)
	}
	if body, err := os.ReadFile(archive); err != nil || string(body) != "export" {
		t.Fatalf("completed archive changed: %q, %v", body, err)
	}
	if _, err := os.Stat(upload); !os.IsNotExist(err) {
		t.Fatalf("abandoned upload survived: %v", err)
	}
}

type zipEntry struct {
	name string
	body []byte
}

func readZipEntries(t *testing.T, path string) []zipEntry {
	t.Helper()
	zr, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	var out []zipEntry
	for _, zf := range zr.File {
		rc, err := zf.Open()
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(rc)
		_ = rc.Close()
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, zipEntry{name: zf.Name, body: body})
	}
	return out
}

func writeZipEntries(t *testing.T, path string, entries []zipEntry) {
	t.Helper()
	f, err := os.Create(path + ".tmp")
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for _, e := range entries {
		w, err := zw.Create(e.name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(e.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path+".tmp", path); err != nil {
		t.Fatal(err)
	}
}

func corruptZipEntry(t *testing.T, path, name string, body []byte) {
	t.Helper()
	entries := readZipEntries(t, path)
	for i := range entries {
		if entries[i].name == name {
			entries[i].body = body
		}
	}
	writeZipEntries(t, path, entries)
}

// A renamed product must not orphan archives made before the rename: the export
// suffix is new, and import still accepts the old one. This is the data-format
// half of the rename, kept separate from display names.
func TestLegacyTwprojArchiveStillImports(t *testing.T) {
	dir, st, _ := seedProject(t)
	archive := filepath.Join(t.TempDir(), "legacy-export.twproj")
	if _, err := ExportProject(dir, st, archive, "test"); err != nil {
		t.Fatalf("ExportProject: %v", err)
	}
	got, err := ImportProject(archive, t.TempDir())
	if err != nil {
		t.Fatalf("a .twproj archive was refused: %v", err)
	}
	if got.Project.ID != "p-test" {
		t.Fatalf("imported id = %s", got.Project.ID)
	}
}

// Boot sweep after an interrupted upload must clean both the new staging suffix
// and the one a previous build left behind.
func TestCleanTransferRootRemovesBothUploadSuffixes(t *testing.T) {
	data := t.TempDir()
	root := filepath.Join(data, ".transfer")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	legacy := filepath.Join(root, "upload-old.twproj")
	current := filepath.Join(root, "upload-new.sakerproj")
	for _, p := range []string{legacy, current} {
		if err := os.WriteFile(p, []byte("partial"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := CleanTransferRoot(data); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{legacy, current} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("abandoned upload survived: %s: %v", p, err)
		}
	}
}