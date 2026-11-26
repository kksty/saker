package bundle

import (
	"archive/zip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/kksty/saker/internal/evidence"
	"github.com/kksty/saker/internal/store"
)

// ProjectFormat identifies a whole-project transfer, as opposed to the
// fact/evidence-only bundle. The two formats share a filename convention but
// not a reader: a case bundle lacks project.json and the SQLite ledger, so
// treating one as the other would either fail halfway or create an empty
// project.
const ProjectFormat = "saker-project"

// ProjectVersion is the project archive format version. Bump only when an old
// reader would misread a new archive.
const ProjectVersion = 1

// Import bounds. They are far above a real engagement (hundreds of MB, tens of
// thousands of files) but stop a hand-made archive from filling the disk through
// a declared-huge central directory before the first byte is written.
const (
	maxProjectArchiveFiles  = 1_000_000
	maxProjectArchiveBytes  = 64 << 30
	maxProjectManifestBytes = 32 << 20
	projectTransferDir      = ".transfer"
)

// ProjectRef is the identity a receiver needs before extracting anything.
type ProjectRef struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	Created       time.Time `json:"created"`
	SchemaVersion int       `json:"schema_version"`
}

// ProjectCounts is the human-readable summary shown before an import. File and
// byte counts describe the archive, not the project directory: excluded runtime
// files are deliberately absent.
type ProjectCounts struct {
	Intents  int   `json:"intents"`
	Facts    int   `json:"facts"`
	Evidence int   `json:"evidence"`
	Runs     int   `json:"runs"`
	Files    int   `json:"files"`
	Bytes    int64 `json:"bytes"`
}

// ProjectManifest describes one exported project directory.
//
// Files carries a SHA-256 per entry so a dropped or corrupted transfer is
// refused instead of imported as a project that merely looks complete. Missing
// records evidence rows whose original was already gone at export time; the
// ledger keeps the row, and the receiver shows the same gap rather than
// inventing proof.
type ProjectManifest struct {
	Format        string        `json:"format"`
	Version       int           `json:"version"`
	ExportedAt    time.Time     `json:"exported_at"`
	SourceVersion string        `json:"source_version,omitempty"`
	Project       ProjectRef    `json:"project"`
	Counts        ProjectCounts `json:"counts"`
	Files         []File        `json:"files"`
	Missing       []string      `json:"missing,omitempty"`
}

// projectDoc is the subset of project.json export needs. Everything else in the
// manifest (scope, description, model defaults, policy) travels as opaque bytes:
// the transfer layer must not become a second owner of the engagement schema.
type projectDoc struct {
	Name    string    `json:"name"`
	Created time.Time `json:"created"`
}

// ExportProject writes one portable archive of a project. The SQLite ledger is
// snapshotted with VACUUM INTO rather than copied: a live main file without its
// WAL can silently omit committed rows, which is exactly the failure a project
// transfer must not have.
//
// The caller is responsible for quiescing the project (paused and no live
// runs). Export also refuses a digest conflict, because carrying altered bytes
// under an unchanged evidence row would launder tampering into the receiver's
// ledger.
func ExportProject(projectDir string, st *store.Store, out, sourceVersion string) (ProjectManifest, error) {
	if st == nil {
		return ProjectManifest{}, errors.New("项目账本未打开")
	}
	rawProject, err := os.ReadFile(filepath.Join(projectDir, "project.json"))
	if err != nil {
		return ProjectManifest{}, fmt.Errorf("读项目清单失败：%w", err)
	}
	var doc projectDoc
	if err := json.Unmarshal(rawProject, &doc); err != nil {
		return ProjectManifest{}, fmt.Errorf("项目清单不是合法 JSON：%w", err)
	}
	// The directory name is the project's address: the loader overrides a
	// manifest copied from another project with it, so the transfer must use the
	// same authority instead of exporting a name the receiver will not keep.
	id := filepath.Base(filepath.Clean(projectDir))
	if err := safeProjectID(id); err != nil {
		return ProjectManifest{}, err
	}
	if doc.Created.IsZero() {
		if info, statErr := os.Stat(projectDir); statErr == nil {
			doc.Created = info.ModTime()
		}
	}

	snap := st.Snapshot()
	m := ProjectManifest{
		Format:        ProjectFormat,
		Version:       ProjectVersion,
		ExportedAt:    time.Now(),
		SourceVersion: sourceVersion,
		Project: ProjectRef{
			ID:            id,
			Name:          strings.TrimSpace(doc.Name),
			Created:       doc.Created,
			SchemaVersion: snap.Version,
		},
		Counts: ProjectCounts{
			Intents:  len(snap.Intents),
			Facts:    len(snap.Facts),
			Evidence: len(snap.Evidence),
			Runs:     len(snap.Runs),
		},
	}
	if m.Project.Name == "" {
		m.Project.Name = id
	}

	caseRoot := filepath.Join(projectDir, "cases")
	missing, err := projectEvidenceGaps(caseRoot, snap)
	if err != nil {
		return m, err
	}
	m.Missing = missing

	// A private temp file means a failed VACUUM cannot leave a half-written
	// ledger where a later retry would mistake it for a complete backup.
	tmpDir, err := os.MkdirTemp("", "saker-project-ledger-")
	if err != nil {
		return m, err
	}
	defer os.RemoveAll(tmpDir)
	ledgerCopy := filepath.Join(tmpDir, "ledger.sqlite")
	if err := st.BackupTo(ledgerCopy); err != nil {
		return m, fmt.Errorf("导出项目账本：%w", err)
	}

	entries := []struct{ name, abs string }{
		{"project.json", filepath.Join(projectDir, "project.json")},
		{"ledger.sqlite", ledgerCopy},
	}
	addTop := func(name string) {
		p := filepath.Join(projectDir, name)
		if info, statErr := os.Stat(p); statErr == nil && info.Mode().IsRegular() {
			entries = append(entries, struct{ name, abs string }{name, p})
		}
	}
	addTop("state.md")
	addTop("decisions.jsonl")
	// The operator's own goal decisions (met/reopen) exist nowhere else once a
	// reopen clears the goal row, so a backup that drops this file would lose
	// the only record of who stamped the goal and why.
	addTop("operator-actions.jsonl")

	if err := walkProjectEntries(filepath.Join(projectDir, "runs"), "runs", func(string) bool { return true }, &entries); err != nil {
		return m, err
	}
	if err := walkProjectEntries(caseRoot, "cases", func(rel string) bool {
		return projectCaseFileWanted(rel)
	}, &entries); err != nil {
		return m, err
	}

	sort.Slice(entries, func(i, j int) bool { return entries[i].name < entries[j].name })
	for _, e := range entries {
		info, statErr := os.Stat(e.abs)
		if statErr != nil || !info.Mode().IsRegular() {
			continue
		}
		m.Counts.Files++
		m.Counts.Bytes += info.Size()
	}

	if err := os.MkdirAll(filepath.Dir(out), 0o700); err != nil {
		return m, err
	}
	partial := out + ".partial"
	_ = os.Remove(partial)
	f, err := os.OpenFile(partial, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return m, err
	}
	ok := false
	defer func() {
		if !ok {
			_ = os.Remove(partial)
		}
	}()
	zw := zip.NewWriter(f)
	for _, e := range entries {
		rec, addErr := addFile(zw, e.name, e.abs)
		if addErr != nil {
			_ = zw.Close()
			_ = f.Close()
			return m, addErr
		}
		m.Files = append(m.Files, rec)
	}
	mb, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		_ = zw.Close()
		_ = f.Close()
		return m, err
	}
	w, err := zw.Create(ProjectManifestFile)
	if err != nil {
		_ = zw.Close()
		_ = f.Close()
		return m, err
	}
	if _, err := w.Write(mb); err != nil {
		_ = zw.Close()
		_ = f.Close()
		return m, err
	}
	if err := zw.Close(); err != nil {
		_ = f.Close()
		return m, err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return m, err
	}
	if err := f.Close(); err != nil {
		return m, err
	}
	if err := os.Rename(partial, out); err != nil {
		return m, err
	}
	ok = true
	return m, nil
}

// ProjectManifestFile is the last entry of a project archive. It is the same
// name as the case bundle's manifest; Format is what tells them apart.
const ProjectManifestFile = ManifestFile

// CreateProjectUpload opens an incoming archive in the private staging area.
// The caller closes and removes it when the request ends.
func CreateProjectUpload(dataDir string) (*os.File, error) {
	root := filepath.Join(dataDir, projectTransferDir)
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, err
	}
	return os.CreateTemp(root, "upload-*.sakerproj")
}

// CleanTransferRoot removes files left by an interrupted import. The caller
// must own the instance's console listener before running this boot-time sweep.
func CleanTransferRoot(dataDir string) error {
	root := filepath.Join(dataDir, projectTransferDir)
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		path := filepath.Join(root, name)
		switch {
		case entry.IsDir() && strings.HasPrefix(name, "project-"):
			if err := os.RemoveAll(path); err != nil {
				return err
			}
		case entry.Type().IsRegular() && strings.HasPrefix(name, "upload-") && (strings.HasSuffix(name, ".sakerproj") || strings.HasSuffix(name, ".twproj")):
			if err := os.Remove(path); err != nil {
				return err
			}
		}
	}
	return nil
}

// ImportProject verifies the complete archive, extracts it into a staging
// directory, checks the offline ledger and every evidence digest, and only then
// publishes <data>/projects/<id>. A rejected import leaves no project behind.
//
// Imported projects are written paused: a transfer is not permission to spend
// tokens, and the receiver must inspect the engagement before resuming it.
func ImportProject(zipPath, dataDir string) (ProjectManifest, error) {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return ProjectManifest{}, fmt.Errorf("打开项目包失败：%w", err)
	}
	defer zr.Close()
	// Check the central directory size before decoding the manifest: the archive
	// is operator-supplied, and a million entries must be refused before they are
	// all read into memory.
	if len(zr.File) > maxProjectArchiveFiles {
		return ProjectManifest{}, fmt.Errorf("项目包条目过多：%d", len(zr.File))
	}

	m, rawProject, err := readProjectManifest(zr)
	if err != nil {
		return ProjectManifest{}, err
	}
	id := strings.TrimSpace(m.Project.ID)
	if err := safeProjectID(id); err != nil {
		return ProjectManifest{}, err
	}

	// The manifest is written last; a zip where it is not the final entry has
	// either been rearranged or truncated after the manifest, and both make the
	// "verify first, extract second" pass unreliable.
	if len(zr.File) == 0 || zr.File[len(zr.File)-1].Name != ProjectManifestFile {
		return ProjectManifest{}, errors.New("项目包的 manifest.json 不是最后一个条目")
	}

	if len(m.Files) > maxProjectArchiveFiles {
		return ProjectManifest{}, fmt.Errorf("项目包条目过多：%d", len(m.Files))
	}
	want := make(map[string]File, len(m.Files))
	for _, f := range m.Files {
		if f.Size < 0 {
			return ProjectManifest{}, fmt.Errorf("项目包条目大小不合法：%s", f.Path)
		}
		if _, err := safeName(f.Path); err != nil {
			return ProjectManifest{}, err
		}
		if _, dup := want[f.Path]; dup {
			return ProjectManifest{}, fmt.Errorf("项目包清单重复条目：%s", f.Path)
		}
		want[f.Path] = f
	}
	if _, ok := want["project.json"]; !ok {
		return ProjectManifest{}, errors.New("项目包缺少 project.json")
	}
	if _, ok := want["ledger.sqlite"]; !ok {
		return ProjectManifest{}, errors.New("项目包缺少 ledger.sqlite")
	}

	// Pass 1: verify every entry before writing anything.
	seen := make(map[string]bool, len(m.Files))
	var total uint64
	for _, zf := range zr.File {
		if zf.Name == ProjectManifestFile {
			continue
		}
		if zf.UncompressedSize64 > maxProjectArchiveBytes || total > maxProjectArchiveBytes-zf.UncompressedSize64 {
			return ProjectManifest{}, fmt.Errorf("项目包解压后过大：超过 %d 字节", int64(maxProjectArchiveBytes))
		}
		total += zf.UncompressedSize64
		if zf.FileInfo().IsDir() {
			return ProjectManifest{}, fmt.Errorf("项目包不支持目录条目：%s", zf.Name)
		}
		if zf.Mode()&os.ModeSymlink != 0 {
			return ProjectManifest{}, fmt.Errorf("项目包不支持符号链接：%s", zf.Name)
		}
		if _, err := safeName(zf.Name); err != nil {
			return ProjectManifest{}, err
		}
		w, ok := want[zf.Name]
		if !ok {
			return ProjectManifest{}, fmt.Errorf("项目包里有清单外的条目：%s", zf.Name)
		}
		if err := verifyEntry(zf, w); err != nil {
			return ProjectManifest{}, err
		}
		seen[zf.Name] = true
	}
	for name := range want {
		if !seen[name] {
			return ProjectManifest{}, fmt.Errorf("项目清单里的文件不在包里：%s", name)
		}
	}

	var doc map[string]json.RawMessage
	if err := json.Unmarshal(rawProject, &doc); err != nil {
		return ProjectManifest{}, fmt.Errorf("project.json 不是合法 JSON：%w", err)
	}
	if doc == nil {
		return ProjectManifest{}, errors.New("project.json 必须是 JSON 对象")
	}
	// project.json's id is a copy, not the address: the loader always uses the
	// directory name, so a manifest carried over from a copied project must not
	// make its own export/reimport fail. The directory name above is the
	// authority; the rewrite below makes the file agree with it.
	transferRoot := filepath.Join(dataDir, projectTransferDir)
	if err := os.MkdirAll(transferRoot, 0o700); err != nil {
		return ProjectManifest{}, err
	}
	staging, err := os.MkdirTemp(transferRoot, "project-*")
	if err != nil {
		return ProjectManifest{}, err
	}
	published := false
	defer func() {
		if !published {
			_ = os.RemoveAll(staging)
		}
	}()

	// Pass 2: extract into staging. Every path has already been checked against
	// the manifest, but safeName is applied again because extraction is the
	// operation that actually writes attacker-influenced names.
	for _, zf := range zr.File {
		if zf.Name == ProjectManifestFile {
			continue
		}
		rel, err := safeName(zf.Name)
		if err != nil {
			return ProjectManifest{}, err
		}
		dst := filepath.Join(staging, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			return ProjectManifest{}, err
		}
		if err := extractEntry(zf, dst); err != nil {
			return ProjectManifest{}, err
		}
	}

	// Pass 3: the archive proves the transfer, not the ledger's invariant. Open
	// the copied ledger read-only and re-check every evidence digest against the
	// extracted bytes. Missing originals are tolerated only when the exporter
	// recorded them; a silent gap would make the receiver believe it has proof
	// the sender knew was gone.
	db, err := store.ReadSnapshot(filepath.Join(staging, "ledger.sqlite"))
	if err != nil {
		return ProjectManifest{}, fmt.Errorf("项目账本不可用：%w", err)
	}
	if m.Project.SchemaVersion != 0 && m.Project.SchemaVersion != db.Version {
		return ProjectManifest{}, fmt.Errorf("项目包清单的账本版本 %d 与实际 %d 不一致", m.Project.SchemaVersion, db.Version)
	}
	gaps, err := projectEvidenceGaps(filepath.Join(staging, "cases"), db)
	if err != nil {
		return ProjectManifest{}, err
	}
	recorded := make(map[string]bool, len(m.Missing))
	for _, uri := range m.Missing {
		recorded[uri] = true
	}
	for _, gap := range gaps {
		if !recorded[gap] {
			return ProjectManifest{}, fmt.Errorf("证据 %s 缺失，但导出清单没有记录", gap)
		}
	}

	// Rewrite the manifest into the only state a fresh receiver may start in:
	// paused, with a machine-readable cause. The exported project.json remains
	// the authority for scope, description, defaults and policy, but a transfer
	// must not silently resume someone else's engagement.
	doc["paused"] = json.RawMessage("true")
	doc["pause_kind"] = json.RawMessage(`"manual_pause"`)
	if rawID, err := json.Marshal(id); err == nil {
		doc["id"] = rawID
	}
	rewritten, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return ProjectManifest{}, err
	}
	if err := os.WriteFile(filepath.Join(staging, "project.json"), append(rewritten, '\n'), 0o600); err != nil {
		return ProjectManifest{}, err
	}

	projectsRoot := filepath.Join(dataDir, "projects")
	if err := os.MkdirAll(projectsRoot, 0o755); err != nil {
		return ProjectManifest{}, err
	}
	target := filepath.Join(projectsRoot, id)
	if _, err := os.Stat(target); err == nil {
		return ProjectManifest{}, fmt.Errorf("项目 %s 已经存在；导入不会覆盖现有项目", id)
	} else if !os.IsNotExist(err) {
		return ProjectManifest{}, err
	}
	if err := os.Rename(staging, target); err != nil {
		return ProjectManifest{}, err
	}
	published = true
	m.Project.ID = id
	return m, nil
}

// walkProjectEntries adds regular files below root under the archive prefix.
// Directories are never entries: the receiver creates them from file paths, and
// a zip directory entry would let a sender describe a tree it did not send.
func walkProjectEntries(root, prefix string, wanted func(rel string) bool, entries *[]struct{ name, abs string }) error {
	if _, err := os.Stat(root); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	return filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			// These trees are judgement/runtime scratch and may be large; there
			// is no durable file below either one, so do not even walk them.
			if d.Name() == ".verify" || d.Name() == ".cyber" {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		rel, relErr := filepath.Rel(root, p)
		if relErr != nil {
			return relErr
		}
		rel = filepath.ToSlash(rel)
		if !wanted(rel) {
			return nil
		}
		*entries = append(*entries, struct{ name, abs string }{prefix + "/" + rel, p})
		return nil
	})
}

// projectCaseFileWanted is the durable subset of a case tree.
//
// .verify/.cyber are judgement/runtime state; .saker is mostly the handoff channel
// and per-run credentials. Worker notes, fact declarations, consumption cursors
// and explicit repair audits are durable: losing them prevents cross-run memory
// and makes a stranded declaration impossible to diagnose or recover. Login materials (.access) travel only
// as metadata and material; live leases and control sessions belong to the old
// machine. Native artifacts (.native) do travel: the console's artifact view
// points at them.
func projectCaseFileWanted(rel string) bool {
	parts := strings.Split(filepath.ToSlash(rel), "/")
	if len(parts) == 0 {
		return false
	}
	for _, part := range parts {
		if part == ".verify" || part == ".cyber" {
			return false
		}
	}
	base := parts[len(parts)-1]
	hasHandoff, hasAccess := false, false
	for _, part := range parts {
		switch part {
		case ".saker":
			hasHandoff = true
		case ".access":
			hasAccess = true
		}
	}
	if hasHandoff {
		return base == "worker-note.md" || base == "facts.jsonl" || base == "facts.offset" || strings.HasPrefix(base, "cursor-repair-") && strings.HasSuffix(base, ".json")
	}
	if hasAccess {
		switch base {
		case "entry.json", "material.json", "account.json":
			return true
		}
		return false
	}
	return true
}

// projectEvidenceGaps returns the rows whose original is gone or outside the
// case root, and refuses a row whose bytes no longer match its recorded digest.
// Export and import share it so both sides answer "is this proof still here?"
// the same way.
func projectEvidenceGaps(caseRoot string, db store.DB) ([]string, error) {
	var missing []string
	seen := map[string]bool{}
	for _, e := range db.Evidence {
		err := evidence.Verify(caseRoot, e)
		switch {
		case err == nil:
		case errors.Is(err, evidence.ErrMissing), errors.Is(err, evidence.ErrOutside):
			key := strings.TrimSpace(e.URI)
			if key == "" {
				key = e.ID
			}
			if !seen[key] {
				seen[key] = true
				missing = append(missing, key)
			}
		case errors.Is(err, evidence.ErrDigestMismatch):
			return nil, fmt.Errorf("%w：证据 %s（%s）的字节与登记摘要不符，项目包不能导出或导入", evidence.ErrDigestMismatch, e.ID, e.URI)
		default:
			return nil, fmt.Errorf("检查证据 %s（%s）：%w", e.ID, e.URI, err)
		}
	}
	return missing, nil
}

// readProjectManifest reads the archive's manifest and the project.json bytes in
// one pass, before any extraction.
func readProjectManifest(zr *zip.ReadCloser) (ProjectManifest, []byte, error) {
	var m ProjectManifest
	var rawProject []byte
	found := false
	for _, zf := range zr.File {
		switch zf.Name {
		case ProjectManifestFile:
			body, err := readLimited(zf, maxProjectManifestBytes)
			if err != nil {
				return m, nil, err
			}
			if err := json.Unmarshal(body, &m); err != nil {
				return m, nil, fmt.Errorf("%s 不是合法 JSON", ProjectManifestFile)
			}
			found = true
		case "project.json":
			body, err := readLimited(zf, maxProjectManifestBytes)
			if err != nil {
				return m, nil, err
			}
			rawProject = body
		}
	}
	if !found {
		return m, nil, fmt.Errorf("项目包里没有 %s", ProjectManifestFile)
	}
	if m.Format != ProjectFormat {
		return m, nil, fmt.Errorf("不是Saker项目包（format=%q）", m.Format)
	}
	if m.Version != ProjectVersion {
		return m, nil, fmt.Errorf("项目包版本 %d 不受支持（当前 %d）", m.Version, ProjectVersion)
	}
	if len(rawProject) == 0 {
		return m, nil, errors.New("项目包缺少 project.json")
	}
	return m, rawProject, nil
}

// readLimited prevents a hand-made archive from turning a metadata entry into an
// unbounded allocation. The manifest and project.json are documents, not
// evidence; both are far below this bound in a real project.
func readLimited(zf *zip.File, limit int64) ([]byte, error) {
	rc, err := zf.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	body, err := io.ReadAll(io.LimitReader(rc, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("项目包元数据过大：%s", zf.Name)
	}
	return body, nil
}

// safeProjectID keeps a manifest from naming a path outside projects/. Existing
// ids are short lowercase strings (p-xxxxxxxx / default); the check is
// deliberately structural rather than a rewrite, so a hand-created project can
// still round-trip under its own id.
func safeProjectID(id string) error {
	// "import" is the one path segment the project API reserves for the archive
	// endpoint; a project with that id could not be edited or deleted.
	if id == "" || id == "." || id == ".." || id == "import" || len(id) > 64 {
		return fmt.Errorf("项目 id 不合法：%q", id)
	}
	if strings.HasPrefix(id, ".") || strings.HasSuffix(id, ".") || strings.HasSuffix(id, " ") || reservedWindowsName(id) {
		return fmt.Errorf("项目 id 在目标系统不可用：%q", id)
	}
	for _, r := range id {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			continue
		}
		return fmt.Errorf("项目 id 含有不允许的字符：%q", id)
	}
	return nil
}
