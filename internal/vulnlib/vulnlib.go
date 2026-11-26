// Package vulnlib is the instance-level vulnerability library.
//
// The engagement is disposable, the finding is not. Every project mirrors its
// `vuln` facts here as they are declared, and deleting a project removes
// everything under projects/<id>/ but leaves these entries (D34). That is the
// one exception to "delete means delete", and it is deliberate: a confirmed
// vulnerability is the part of an engagement that stays useful after the client
// is gone.
//
// Evidence is copied in at capture time rather than referenced, because the
// project workspace it lives in is exactly what deletion destroys. The copy is
// capped: a vulnerability's proof is a minimal reproducer, and the library must
// not silently duplicate a firmware image.
package vulnlib

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/kksty/saker/internal/evidence"
	"github.com/kksty/saker/internal/model"
)

const (
	// FileName holds the library index.
	FileName = "vulns.json"
	// EvidenceDir is where copied evidence lives, below the library root.
	EvidenceDir = "evidence"
	// MaxEvidenceCopy caps one copied file. Larger files are recorded in
	// evidence_dropped instead of being duplicated.
	MaxEvidenceCopy = 32 << 20
)

// Review is the latest verifier verdict copied from the project ledger. It is a
// copy, not a reference: deleting the project deletes the ledger it lives in,
// and the library has to remain able to say why the entry is in its state (D105).
type Review struct {
	Verdict     string    `json:"verdict"` // pass|needs_evidence|reject
	Notes       string    `json:"notes,omitempty"`
	VerifierRun string    `json:"verifier_run,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

// Entry is one vulnerability, attributed to the project that found it.
type Entry struct {
	ID string `json:"id"`
	// ProjectID and Project are captured at declaration time. They outlive the
	// project: after a delete the entry still says where it came from.
	ProjectID string `json:"project_id"`
	Project   string `json:"project,omitempty"`
	// SourceID is the fact id in that project, kept for traceability.
	SourceID   string `json:"source_id"`
	IntentID   string `json:"intent_id,omitempty"`
	Intent     string `json:"intent,omitempty"`
	Title      string `json:"title"`
	Summary    string `json:"summary,omitempty"`
	Doc        string `json:"doc,omitempty"`
	Confidence string `json:"confidence,omitempty"`
	// Severity is the fact's optional grade at the time this entry was written
	// (critical|high|medium|low; empty = ungraded). It is a copy of the ledger
	// label, not a second judgement: the library never re-maps or invents it.
	Severity string `json:"severity,omitempty"`
	// Proof says how the conclusion is reproduced: dynamic|static (D35).
	Proof  string `json:"proof,omitempty"`
	Status string `json:"status"`
	// Review is the latest verdict copied from the project ledger, so the
	// library can still explain the status after the project is deleted (D105).
	Review *Review `json:"review,omitempty"`
	// Evidence paths are relative to the library root.
	Evidence []string `json:"evidence,omitempty"`
	Dropped  []string `json:"evidence_dropped,omitempty"`
	// CreatedAt is when the project declared it; UpdatedAt follows the fact's
	// status (unverified → verified / rejected).
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	// Orphaned is set when the owning project is deleted.
	Orphaned bool `json:"orphaned,omitempty"`
}

type file struct {
	Version int     `json:"version"`
	Vulns   []Entry `json:"vulns"`
}

// Library is the on-disk index plus its evidence directory.
type Library struct {
	root string
	path string

	mu    sync.Mutex
	byKey map[string]*Entry
	order []string
}

// ErrCorrupt marks an index that exists but cannot be parsed. It is the one
// open failure a caller may recover from: the bytes are still on disk, so they
// can be moved aside and the instance can start with an empty library. Every
// other error (a read failure, a permission problem) is not this: an index that
// cannot be read cannot be written safely either, and hiding that would lose
// later declarations silently.
var ErrCorrupt = errors.New("漏洞库索引无法解析")

// Open loads (or creates) the library below dir.
func Open(dir string) (*Library, error) {
	if err := os.MkdirAll(filepath.Join(dir, EvidenceDir), 0o755); err != nil {
		return nil, err
	}
	l := &Library{root: dir, path: filepath.Join(dir, FileName), byKey: map[string]*Entry{}}
	b, err := os.ReadFile(l.path)
	if err != nil {
		if os.IsNotExist(err) {
			return l, nil
		}
		return nil, err
	}
	var f file
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("%w: parse %s: %v", ErrCorrupt, l.path, err)
	}
	// "Parses" is not "is an index". json.Unmarshal accepts `null` (leaves the
	// struct empty), and any JSON object whose fields we do not know (e.g.
	// `{"entries":5}`) also succeeds with a zero value — both would read as a
	// healthy empty library and silently hide every entry that was there. The
	// writer always stamps version 1 (flush), so a version below 1 is not our
	// shape and is damage, exactly like a syntax error.
	if f.Version < 1 {
		return nil, fmt.Errorf("%w: %s 不是有效索引（version=%d；应为 {\"version\":1,\"vulns\":[...]}）", ErrCorrupt, l.path, f.Version)
	}
	for i := range f.Vulns {
		e := f.Vulns[i]
		key := keyOf(e.ProjectID, e.SourceID)
		l.byKey[key] = &e
		l.order = append(l.order, key)
	}
	return l, nil
}

func keyOf(projectID, sourceID string) string { return projectID + "\x00" + sourceID }

// QuarantineCorruptIndex moves an unparseable index aside and returns its new
// path. The bytes are kept: the file is renamed, never truncated or deleted, so
// an operator can still salvage entries by hand after a bad write.
//
// A name already taken in the same second gets a numeric suffix: two corrupt
// files within a second must not make the second quarantine overwrite the first.
// Only a corrupt index is quarantined — the caller checks ErrCorrupt first,
// because moving a readable-but-unreadable file would just lose the index.
func QuarantineCorruptIndex(dir string) (string, error) {
	path := filepath.Join(dir, FileName)
	base := path + ".corrupt-" + time.Now().Format("20060102-150405")
	for i := 1; i <= 100; i++ {
		dst := base
		if i > 1 {
			dst = fmt.Sprintf("%s-%d", base, i)
		}
		if _, err := os.Stat(dst); err == nil {
			continue
		} else if !os.IsNotExist(err) {
			return "", err
		}
		if err := os.Rename(path, dst); err != nil {
			return "", err
		}
		return dst, nil
	}
	return "", fmt.Errorf("%s.corrupt-* 已有 100 个同名备份，未覆盖任何一个", path)
}

// Upsert records one declaration. The same fact updated twice (unverified →
// verified) keeps its id and creation time, so a re-scan does not create a
// second row for the same finding.
func (l *Library) Upsert(e Entry) (Entry, error) {
	if e.ProjectID == "" || e.SourceID == "" || e.Title == "" {
		return Entry{}, fmt.Errorf("漏洞条目缺 project_id / source_id / title")
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	key := keyOf(e.ProjectID, e.SourceID)
	now := time.Now()
	if e.UpdatedAt.IsZero() {
		e.UpdatedAt = now
	}
	if old, ok := l.byKey[key]; ok {
		e.ID = old.ID
		e.CreatedAt = old.CreatedAt
		e.Orphaned = old.Orphaned
		if e.CreatedAt.IsZero() {
			e.CreatedAt = now
		}
		*old = e
	} else {
		e.ID = newID()
		if e.CreatedAt.IsZero() {
			e.CreatedAt = now
		}
		cp := e
		l.byKey[key] = &cp
		l.order = append(l.order, key)
	}
	if err := l.flush(); err != nil {
		return e, err
	}
	return e, nil
}

// KeepEvidence copies the named files (paths relative to caseRoot) into the
// library and returns their library-relative paths plus the ones it refused to
// copy. Files that do not exist are listed as dropped, not silently ignored.
//
// Only a preserved evidence URI (.evidence/objects/<sha256>) is accepted: its
// digest is the address, so the copy can be re-hashed and a tampered library
// object is rebuilt or dropped rather than kept. A working-path URI carries no
// address a reader could check, so it is dropped instead of copied as proof
// nothing can prove.
func (l *Library) KeepEvidence(projectID, caseRoot string, uris []string) (kept, dropped []string) {
	for _, uri := range uris {
		if strings.TrimSpace(uri) == "" {
			continue
		}
		digest, preserved := evidence.ObjectDigest(uri)
		if !preserved {
			dropped = append(dropped, uri)
			continue
		}
		src := filepath.Join(caseRoot, filepath.FromSlash(uri))
		info, err := os.Stat(src)
		if err != nil || info.IsDir() {
			dropped = append(dropped, uri)
			continue
		}
		if info.Size() > MaxEvidenceCopy {
			dropped = append(dropped, uri)
			continue
		}
		rel := filepath.ToSlash(filepath.Join(EvidenceDir, projectID, filepath.FromSlash(uri)))
		dst := filepath.Join(l.root, filepath.FromSlash(rel))
		// An earlier hook call may have copied this object already; re-hash it so
		// a tampered library copy is rebuilt or dropped, never kept.
		if got, err := fileDigest(dst); err != nil || got != digest {
			if err := evidence.CopyTo(caseRoot, model.Evidence{URI: uri, Digest: digest}, dst); err != nil {
				_ = os.Remove(dst)
				dropped = append(dropped, uri)
				continue
			}
		}
		kept = append(kept, rel)
	}
	return kept, dropped
}

// fileDigest hashes an already-copied library file. It is a local helper: the
// library copy is evidence of a project fact, so it must be equivalent to the
// preserved object before the row says "kept".
func fileDigest(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// MarkOrphaned flags every entry of a deleted project. The rows stay.
func (l *Library) MarkOrphaned(projectID string) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, key := range l.order {
		e := l.byKey[key]
		if e.ProjectID != projectID || e.Orphaned {
			continue
		}
		e.Orphaned = true
		e.UpdatedAt = time.Now()
		n++
	}
	if n == 0 {
		return 0, nil
	}
	return n, l.flush()
}

// List returns entries newest first, optionally filtered by project and by a
// case-insensitive substring of the title, summary and doc.
func (l *Library) List(projectID, q string, limit int) []Entry {
	l.mu.Lock()
	out := make([]Entry, 0, len(l.order))
	for _, key := range l.order {
		e := *l.byKey[key]
		if !keep(e, projectID, q) {
			continue
		}
		out = append(out, e)
	}
	l.mu.Unlock()

	sort.SliceStable(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// Count returns how many entries List would keep for the same filters, ignoring
// the limit. The console reports "matched" next to a paged list, so deriving
// the number from a limited page would undercount on every page but a full one.
func (l *Library) Count(projectID, q string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, key := range l.order {
		if keep(*l.byKey[key], projectID, q) {
			n++
		}
	}
	return n
}

// keep is the single filter predicate shared by List and Count. If the two had
// their own copies, a filter change to one would silently make the displayed
// count disagree with the rows.
func keep(e Entry, projectID, q string) bool {
	if projectID != "" && e.ProjectID != projectID {
		return false
	}
	if q != "" && !match(e, q) {
		return false
	}
	return true
}

func match(e Entry, q string) bool {
	q = strings.ToLower(q)
	return strings.Contains(strings.ToLower(e.Title), q) ||
		strings.Contains(strings.ToLower(e.Summary), q) ||
		strings.Contains(strings.ToLower(e.Doc), q)
}

func (l *Library) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.order)
}

// EvidencePath resolves a library-relative evidence path (as stored in Entry,
// e.g. evidence/<project>/<file>) and refuses anything outside the evidence
// directory.
func (l *Library) EvidencePath(rel string) (string, bool) {
	base := filepath.Join(l.root, EvidenceDir)
	abs := filepath.Join(l.root, filepath.FromSlash(rel))
	if !strings.HasPrefix(abs, base+string(os.PathSeparator)) {
		return "", false
	}
	if _, err := os.Stat(abs); err != nil {
		return "", false
	}
	return abs, true
}

func (l *Library) flush() error {
	entries := make([]Entry, 0, len(l.order))
	for _, key := range l.order {
		entries = append(entries, *l.byKey[key])
	}
	b, err := json.MarshalIndent(file{Version: 1, Vulns: entries}, "", "  ")
	if err != nil {
		return err
	}
	tmp := l.path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, l.path)
}

func newID() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return "gv_" + hex.EncodeToString(b[:])
}
