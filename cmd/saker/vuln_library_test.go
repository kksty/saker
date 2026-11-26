package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kksty/saker/internal/model"
	"github.com/kksty/saker/internal/project"
	"github.com/kksty/saker/internal/vulnlib"
)

// A truncated index must not stop the instance: the file is renamed aside, the
// bytes are kept, and the library comes back empty but usable. This is the
// regression for the startup path that used to log.Fatalf here, taking every
// project down because one secondary index was half-written.
func TestCorruptVulnIndexIsQuarantinedAndStartupContinues(t *testing.T) {
	root := filepath.Join(t.TempDir(), "vulns")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	original := []byte(`{"version":2,"vulns":[{"id":"v1","project_id":"p","source_id":"s","title":"半截条目"`)
	index := filepath.Join(root, vulnlib.FileName)
	if err := os.WriteFile(index, original, 0o644); err != nil {
		t.Fatal(err)
	}

	lib, notice, err := openVulnLibrary(root)
	if err != nil {
		t.Fatalf("损坏索引不应中断启动：%v", err)
	}
	if lib == nil {
		t.Fatal("返回了 nil 库：hub 对 h.vulns 没有 nil 检查")
	}
	if n := lib.Len(); n != 0 {
		t.Fatalf("Len = %d，空库应为 0", n)
	}
	if _, err := os.Stat(index); !os.IsNotExist(err) {
		t.Fatalf("原索引仍在原处，未改名：err=%v", err)
	}

	matches, err := filepath.Glob(index + ".corrupt-*")
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 {
		t.Fatalf("备份文件 = %v，应恰好 1 个", matches)
	}
	kept, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(kept, original) {
		t.Fatalf("备份字节被改动：%q", kept)
	}
	// The quarantine is also reported, not only logged: the console shows it
	// next to skipped_projects, so the path must be the file that now exists.
	if notice == nil {
		t.Fatal("隔离后没有返回开机状态通知：操作员只能在日志里看到")
	}
	if notice.Path != matches[0] {
		t.Fatalf("通知里的路径 %q 不是实际备份 %q", notice.Path, matches[0])
	}
	if notice.Reason == "" || notice.At.IsZero() {
		t.Fatalf("通知缺原因或时间：%+v", notice)
	}

	// The empty library must be writable, or the quarantine traded one outage
	// for another: a new declaration has to land in a fresh index.
	if _, err := lib.Upsert(vulnlib.Entry{ProjectID: "p", SourceID: "s", Title: "新的条目"}); err != nil {
		t.Fatalf("隔离后写入失败：%v", err)
	}
	if _, err := os.Stat(index); err != nil {
		t.Fatalf("新索引没有写出来：%v", err)
	}

	// And the next start reads that new index instead of quarantining again:
	// "the empty library took over" only holds if a restart keeps what was
	// written after the quarantine. Without this, an implementation that
	// re-quarantined on every boot would pass the assertions above.
	reopened, notice2, err := openVulnLibrary(root)
	if err != nil {
		t.Fatalf("第二次启动报错：%v", err)
	}
	if notice2 != nil {
		t.Fatalf("第二次启动又隔离了一次：%+v", notice2)
	}
	if n := reopened.Len(); n != 1 {
		t.Fatalf("第二次启动读到 %d 条，应为 1（新条目没接上）", n)
	}
	if matches, _ := filepath.Glob(index + ".corrupt-*"); len(matches) != 1 {
		t.Fatalf("备份文件 %v，应仍只有第一次那一个", matches)
	}
}

// Positive control for the same code path: a healthy index still loads its
// entries, and nothing is renamed. Without this, the test above would also pass
// on a helper that simply emptied every library it was given.
func TestHealthyVulnIndexLoadsWithoutQuarantine(t *testing.T) {
	root := filepath.Join(t.TempDir(), "vulns")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	index := filepath.Join(root, vulnlib.FileName)
	body := []byte(`{"version":2,"vulns":[{"id":"v1","project_id":"p","source_id":"s","title":"保留的条目","status":"unverified"}]}`)
	if err := os.WriteFile(index, body, 0o644); err != nil {
		t.Fatal(err)
	}

	lib, notice, err := openVulnLibrary(root)
	if err != nil {
		t.Fatalf("健康索引不应报错：%v", err)
	}
	if n := lib.Len(); n != 1 {
		t.Fatalf("Len = %d，应为 1", n)
	}
	if _, err := os.Stat(index); err != nil {
		t.Fatalf("健康索引被动了：%v", err)
	}
	if matches, _ := filepath.Glob(index + ".corrupt-*"); len(matches) != 0 {
		t.Fatalf("健康索引却产生了备份：%v", matches)
	}
	if notice != nil {
		t.Fatalf("健康索引却报告了隔离：%+v", notice)
	}
}

// A missing index is the first run, not damage: empty library, no quarantine.
func TestMissingVulnIndexStartsEmpty(t *testing.T) {
	root := filepath.Join(t.TempDir(), "vulns")
	lib, notice, err := openVulnLibrary(root)
	if err != nil {
		t.Fatalf("首次启动不应报错：%v", err)
	}
	if lib == nil || lib.Len() != 0 {
		t.Fatalf("空库应为 0 条：%v", lib)
	}
	if matches, _ := filepath.Glob(filepath.Join(root, vulnlib.FileName) + ".corrupt-*"); len(matches) != 0 {
		t.Fatalf("没有索引却产生了备份：%v", matches)
	}
	if notice != nil {
		t.Fatalf("首次启动却报告了隔离：%+v", notice)
	}
}

// A read failure that is not a parse failure must stay fatal: an index that
// cannot be read cannot be written safely either, and quarantining it would
// silently lose the library. Here the index path is a directory, which is the
// cheapest portable way to make the read fail for a reason that is not "corrupt".
func TestUnreadableVulnIndexIsNotQuarantined(t *testing.T) {
	root := filepath.Join(t.TempDir(), "vulns")
	index := filepath.Join(root, vulnlib.FileName)
	if err := os.MkdirAll(index, 0o755); err != nil {
		t.Fatal(err)
	}
	lib, notice, err := openVulnLibrary(root)
	if err == nil {
		t.Fatalf("读不了的索引应报错，得到 lib=%v", lib)
	}
	if matches, _ := filepath.Glob(index + ".corrupt-*"); len(matches) != 0 {
		t.Fatalf("非损坏错误不应触发隔离：%v", matches)
	}
	if notice != nil {
		t.Fatalf("非损坏错误却报告了隔离：%+v", notice)
	}
	if _, statErr := os.Stat(index); statErr != nil {
		t.Fatalf("非损坏错误却动了索引：%v", statErr)
	}
}

// The acceptance requirement behind the quarantine: a corrupt index must not
// cost the library any evidence it already holds. Startup replays the ledger
// into the now-empty library (hub.backfill → recordVuln → KeepEvidence), so the
// copies on disk have to survive that replay unchanged — the crash that
// truncated the index must not also become the event that deletes originals.
func TestCorruptVulnIndexKeepsLibraryEvidenceOnRestart(t *testing.T) {
	data := t.TempDir()
	mgr, err := project.OpenPooled(filepath.Join(data, "projects"))
	if err != nil {
		t.Fatal(err)
	}
	p, err := mgr.Create("kept", "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Store.Close() })

	// One preserved object in the case, addressed by its digest as usual.
	body := []byte("fixture evidence bytes")
	sum := sha256.Sum256(body)
	digest := hex.EncodeToString(sum[:])
	uri := ".evidence/objects/" + digest
	caseObject := filepath.Join(p.CaseDir, filepath.FromSlash(uri))
	if err := os.MkdirAll(filepath.Dir(caseObject), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(caseObject, body, 0o600); err != nil {
		t.Fatal(err)
	}
	ev, err := p.Store.AddEvidence(model.Evidence{Name: "fixture.txt", URI: uri, Digest: digest, Size: int64(len(body))})
	if err != nil {
		t.Fatal(err)
	}
	fact, err := p.Store.AddFact(model.Fact{Kind: "vuln", Title: "fixture vuln", Status: "unverified", EvidenceIDs: []string{ev.ID}})
	if err != nil {
		t.Fatal(err)
	}

	// The library already holds that copy (an earlier run wrote it) and its
	// index is truncated — the state a crash mid-write leaves behind. An extra
	// original no current fact references sits next to it: the library keeps
	// copies whose fact was deleted or rewritten, and a restart must not sweep
	// them just because the rebuilt index no longer mentions them.
	root := filepath.Join(data, "vulns")
	libObject := filepath.Join(root, vulnlib.EvidenceDir, p.ID, filepath.FromSlash(uri))
	if err := os.MkdirAll(filepath.Dir(libObject), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(libObject, body, 0o600); err != nil {
		t.Fatal(err)
	}
	extra := filepath.Join(root, vulnlib.EvidenceDir, p.ID, "kept-from-an-earlier-engagement.txt")
	if err := os.WriteFile(extra, []byte("unreferenced original"), 0o600); err != nil {
		t.Fatal(err)
	}
	index := filepath.Join(root, vulnlib.FileName)
	if err := os.WriteFile(index, []byte(`{"version":2,"vulns":[`), 0o644); err != nil {
		t.Fatal(err)
	}
	before := countEvidenceFiles(t, filepath.Join(root, vulnlib.EvidenceDir))
	if before != 2 {
		t.Fatalf("fixture: 原件数 = %d，应为 2", before)
	}

	lib, notice, err := openVulnLibrary(root)
	if err != nil {
		t.Fatalf("损坏索引不应中断启动：%v", err)
	}
	if notice == nil {
		t.Fatal("这条用例的前提是坏索引，必须带回隔离通知")
	}
	if n := lib.Len(); n != 0 {
		t.Fatalf("隔离后应是空库，Len = %d", n)
	}

	// What the hub does on every start: replay the ledger into the library.
	h := &hub{vulns: lib}
	h.backfill(p)

	after := countEvidenceFiles(t, filepath.Join(root, vulnlib.EvidenceDir))
	if after != before {
		t.Fatalf("重启回放后原件数 %d → %d，原件被删了", before, after)
	}
	for _, want := range []string{libObject, extra} {
		if _, err := os.Stat(want); err != nil {
			t.Fatalf("原有原件不见了：%s：%v", want, err)
		}
	}
	// The count only means something if the replay really re-attached the
	// evidence: an empty entry would leave the count unchanged for the wrong
	// reason and hide a backend that stopped copying.
	list := lib.List(p.ID, "", 10)
	if len(list) != 1 {
		t.Fatalf("回放后条目数 = %d，应为 1", len(list))
	}
	if len(list[0].Evidence) != 1 || list[0].SourceID != fact.ID {
		t.Fatalf("回放后的条目没有接回原件：%+v", list[0])
	}

	matches, err := filepath.Glob(index + ".corrupt-*")
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 {
		t.Fatalf("备份文件 = %v，应恰好 1 个", matches)
	}
	kept, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(kept, []byte(`{"version":2,"vulns":[`)) {
		t.Fatalf("备份字节被改动：%q", kept)
	}
}

// countEvidenceFiles counts regular files below dir (0 when dir is absent).
func countEvidenceFiles(t *testing.T, dir string) int {
	t.Helper()
	n := 0
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if !d.IsDir() {
			n++
		}
		return nil
	})
	if err != nil {
		t.Fatalf("统计原件失败：%v", err)
	}
	return n
}

// The operator must be able to learn about a quarantined index without reading
// the process log, so it rides on GET /api/projects next to skipped_projects. A
// healthy start reports the field as null (present, empty) rather than omitting
// it: the console has to tell "nothing quarantined now" apart from "this build
// cannot report it".
func TestProjectIndexReportsVulnQuarantine(t *testing.T) {
	m, err := project.OpenPooled(filepath.Join(t.TempDir(), "projects"))
	if err != nil {
		t.Fatal(err)
	}
	p, err := m.Create("Acme", "")
	if err != nil {
		t.Fatal(err)
	}
	// Pooled handles keep the ledger open; close before TempDir cleanup removes
	// the tree (Windows refuses to unlink an open sqlite file).
	t.Cleanup(func() { _ = p.Store.Close() })
	h := &hub{mgr: m, cfg: hubConfig{}, kits: map[string]*kit{}}

	get := func() map[string]any {
		t.Helper()
		w := httptest.NewRecorder()
		h.serveProjects(w, httptest.NewRequest(http.MethodGet, "/api/projects", nil))
		if w.Code != http.StatusOK {
			t.Fatalf("GET /api/projects = %d %s", w.Code, w.Body.String())
		}
		var index map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &index); err != nil {
			t.Fatalf("index = %s", w.Body.String())
		}
		return index
	}

	index := get()
	v, ok := index["vuln_library_quarantine"]
	if !ok {
		t.Fatal("项目索引没有 vuln_library_quarantine 字段：操作员无从得知漏洞库被隔离")
	}
	if v != nil {
		t.Fatalf("健康启动却报告了隔离：%v", v)
	}
	// Positive control for the field's usefulness: skipped_projects is in the
	// same response, so a payload that lost every extra key is still caught.
	if _, ok := index["skipped_projects"]; !ok {
		t.Fatalf("索引缺 skipped_projects，断言可能空跑：%s", index)
	}

	at := time.Now().UTC().Truncate(time.Second)
	h.vulnQuarantine = &vulnQuarantine{Path: filepath.Join("data", "vulns", "vulns.json.corrupt-20261124-151805"), Reason: "parse …: unexpected end of JSON input", At: at}
	index = get()
	got, ok := index["vuln_library_quarantine"].(map[string]any)
	if !ok {
		t.Fatalf("隔离通知没有作为对象下发：%v", index["vuln_library_quarantine"])
	}
	if got["path"] != h.vulnQuarantine.Path {
		t.Fatalf("path = %v，应为 %v", got["path"], h.vulnQuarantine.Path)
	}
	if got["reason"] == "" || got["quarantined_at"] == nil {
		t.Fatalf("隔离通知缺原因或时间：%v", got)
	}
}

// "Parses" is not "is an index": null, a foreign object and a payload without our
// version field all unmarshal without an error and leave a zero struct, which
// would read as a healthy empty library and silently hide every entry the file
// held. They must be treated as damage — quarantined, reported, empty.
func TestUnparseableShapesAreTreatedAsCorrupt(t *testing.T) {
	cases := []struct{ name, body string }{
		{"null", "null"},
		{"empty object", "{}"},
		{"foreign object", `{"entries":5}`},
		{"no version", `{"vulns":[]}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "vulns")
			if err := os.MkdirAll(root, 0o755); err != nil {
				t.Fatal(err)
			}
			index := filepath.Join(root, vulnlib.FileName)
			if err := os.WriteFile(index, []byte(c.body), 0o644); err != nil {
				t.Fatal(err)
			}
			lib, notice, err := openVulnLibrary(root)
			if err != nil {
				t.Fatalf("坏形态不应中断启动：%v", err)
			}
			if notice == nil {
				t.Fatalf("%q 被当成健康空库放行了", c.body)
			}
			if lib == nil || lib.Len() != 0 {
				t.Fatalf("应给出空库，得到 %v", lib)
			}
			if _, err := os.Stat(index); !os.IsNotExist(err) {
				t.Fatalf("坏形态没有改名：err=%v", err)
			}
			matches, _ := filepath.Glob(index + ".corrupt-*")
			if len(matches) != 1 {
				t.Fatalf("备份文件 = %v，应恰好 1 个", matches)
			}
			kept, err := os.ReadFile(matches[0])
			if err != nil || string(kept) != c.body {
				t.Fatalf("备份字节被改动：%q (%v)", kept, err)
			}
		})
	}
}

// The positive control the rule above must not break: a valid index with zero
// entries is a fresh library. If "version < 1 → corrupt" were the whole story
// applied to the wrong signal, this would be quarantined on every first write.
func TestValidEmptyIndexIsNotQuarantined(t *testing.T) {
	root := filepath.Join(t.TempDir(), "vulns")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	index := filepath.Join(root, vulnlib.FileName)
	body := `{"version":1,"vulns":[]}`
	if err := os.WriteFile(index, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	lib, notice, err := openVulnLibrary(root)
	if err != nil {
		t.Fatalf("合法空索引不应报错：%v", err)
	}
	if notice != nil {
		t.Fatalf("合法空索引被隔离了：%+v", notice)
	}
	if lib == nil || lib.Len() != 0 {
		t.Fatalf("应为 0 条空库：%v", lib)
	}
	if matches, _ := filepath.Glob(index + ".corrupt-*"); len(matches) != 0 {
		t.Fatalf("合法空索引产生了备份：%v", matches)
	}
}
