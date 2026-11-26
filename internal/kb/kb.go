// Package kb is the offline technique/CVE library: the thing a run consults
// when it is stuck and there is no web search.
//
// The entries are embedded so a binary carries its own library. At startup they
// are materialised to <data>/kb/builtin; an operator can add their own entries
// under <data>/kb/entries without a rebuild. Both are loaded, a local entry with
// the same id wins, and the merged set is copied into each case under
// .saker/kb/entries so an executor can grep it with the shell it already has. The
// commander searches the same set through the instance-level /api/kb endpoint;
// the case copy is sandbox visibility, not a second library.
package kb

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

//go:embed assets/entries
var assets embed.FS

// The two layers of an instance's library. BuiltinDir is owned by the binary:
// Ensure rewrites it on every start. LocalDir is owned by the operator:
// startup leaves it alone (except the explicit legacy layout migration),
// while operator publication/deletion writes instance overrides there.
const (
	BuiltinDir = "builtin"
	LocalDir   = "entries"
	// OriginBuiltin marks an entry served from the bundle embedded in this
	// binary. It is repository content: only the maintainer's hand-edited
	// internal/kb/assets/entries ships there, never an instance publication.
	OriginBuiltin = "builtin"
	// OriginInstance marks an entry this instance published under <data>/kb.
	// It is instance state and has no packaging path into the repository.
	OriginInstance = "instance"
)

// Entry is one offline knowledge-base record: the searchable fields (title,
// description, detail, commands, tags, source) plus optional applicability and
// provenance used when an instance publishes entries explicitly. Existing JSON
// remains readable.
type Entry struct {
	ID            string     `json:"id"`
	Title         string     `json:"title"`
	Description   string     `json:"description"`
	Detail        string     `json:"detail"`
	Commands      []string   `json:"commands,omitempty"`
	Tags          []string   `json:"tags,omitempty"`
	Source        string     `json:"source,omitempty"`
	CreatedAt     time.Time  `json:"created_at,omitempty"`
	Applicability string     `json:"applicability,omitempty"`
	SourceRefs    []string   `json:"source_refs,omitempty"`
	UpdatedAt     *time.Time `json:"updated_at,omitempty"`
	// Promotion identifies an explicitly published candidate revision. Exports
	// omit this instance-local id; it is not a source or a knowledge claim.
	Promotion string `json:"promotion,omitempty"`
	// Deleted is an instance override hiding an embedded entry. Only an
	// operator delete writes this minimal tombstone.
	Deleted bool `json:"deleted,omitempty"`

	// origin and covers are instance metadata, never serialised: a stored entry
	// file must not claim where a later instance loaded it from.
	origin string
	covers bool
}

// Origin reports which layer served this entry: OriginBuiltin for the bundle
// compiled into the binary, OriginInstance for an instance publication.
func (e Entry) Origin() string { return e.origin }

// CoversBuiltin reports that this instance entry shadows a built-in entry with
// the same id. The built-in text is untouched; only this instance reads the
// override.
func (e Entry) CoversBuiltin() bool { return e.covers }

// hasBuiltin reports whether the embedded bundle owns this id in the current
// snapshot's built-in layer, including ids an instance entry currently shadows.
func (l *Library) hasBuiltin(id string) bool {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.builtin[id]
}

// Library shares a replaceable snapshot. Searches and explicit publication or
// reload are safe concurrently; readers never observe a partially loaded set.
type Library struct {
	mu      sync.RWMutex
	editMu  sync.Mutex
	dir     string
	entries []Entry
	// builtin is the set of ids the embedded bundle owns, whether or not a
	// local entry currently shadows one. Publication consults it to label an
	// override instead of inventing a second kind of entry.
	builtin map[string]bool
}

// Dir is where the entries live on disk; the executor prompt points at it.
func (l *Library) Dir() string {
	if l == nil {
		return ""
	}
	return l.dir
}

// Entries returns a copy, for diagnostics and tests.
func (l *Library) Entries() []Entry {
	entries := l.snapshotEntries()
	out := make([]Entry, 0, len(entries))
	for _, e := range entries {
		if !e.Deleted {
			out = append(out, e)
		}
	}
	return out
}

func (l *Library) snapshotEntries() []Entry {
	if l == nil {
		return nil
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	return cloneEntries(l.entries)
}

// Ensure writes the embedded entries under dir/builtin, rewriting a file only
// when its content differs, prunes built-in files that no longer exist in this
// binary, and makes dir/entries for the operator's own entries. It returns how
// many built-in entries exist afterwards.
func Ensure(dir string) (int, error) {
	if err := os.MkdirAll(filepath.Join(dir, LocalDir), 0o755); err != nil {
		return 0, err
	}
	target := filepath.Join(dir, BuiltinDir)
	if err := os.MkdirAll(target, 0o755); err != nil {
		return 0, err
	}
	count := 0
	keep := map[string]bool{}
	err := fs.WalkDir(assets, "assets/entries", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if !strings.EqualFold(filepath.Ext(p), ".json") {
			return nil
		}
		data, err := assets.ReadFile(p)
		if err != nil {
			return err
		}
		name := filepath.Base(p)
		keep[name] = true
		dst := filepath.Join(target, name)
		if cur, err := os.ReadFile(dst); err == nil && string(cur) == string(data) {
			count++
			return nil
		}
		if err := os.WriteFile(dst, data, 0o644); err != nil {
			return err
		}
		count++
		return nil
	})
	if err != nil {
		return count, err
	}
	// builtin/ belongs to this binary: an entry deleted from the bundle must not
	// linger after an upgrade. A file we cannot remove is reported, not fatal.
	items, err := os.ReadDir(target)
	if err != nil {
		return count, err
	}
	for _, item := range items {
		if item.IsDir() || !strings.HasSuffix(strings.ToLower(item.Name()), ".json") || keep[item.Name()] {
			continue
		}
		if err := os.Remove(filepath.Join(target, item.Name())); err != nil {
			return count, err
		}
	}
	return count, nil
}

// CopyTo loads the source library and materialises its merged snapshot under
// dstDir/entries (usually <case>/.saker/kb). Servers sharing a Library use its
// method instead, so unpublished disk edits do not bypass explicit reload.
func CopyTo(srcDir, dstDir string) error {
	l, err := Open(srcDir)
	if err != nil {
		return err
	}
	return l.CopyTo(dstDir)
}

// CopyTo refreshes only the generated case KB cache. It uses the same merged
// snapshot as the query API, including ID-based overrides and removals. The
// source library and the rest of the case are never modified.
func (l *Library) CopyTo(dstDir string) error {
	entries := l.Entries()
	for _, e := range entries {
		if !validID(e.ID) {
			return fmt.Errorf("无效知识 ID：%s", e.ID)
		}
	}
	dst := filepath.Join(dstDir, LocalDir)
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	wanted := map[string]bool{}
	for _, e := range entries {
		name := e.ID + ".json"
		wanted[name] = true
		e.Promotion = ""
		if err := writeJSONAtomic(filepath.Join(dst, name), e); err != nil {
			return err
		}
	}
	files, err := os.ReadDir(dst)
	if err != nil {
		return err
	}
	for _, file := range files {
		if !file.IsDir() && strings.EqualFold(filepath.Ext(file.Name()), ".json") && !wanted[file.Name()] {
			if err := os.Remove(filepath.Join(dst, file.Name())); err != nil {
				return err
			}
		}
	}
	return nil
}

// Open loads every *.json under dir/builtin and dir/entries. A local entry
// replaces a built-in one with the same id and is reported as an instance
// override. A missing directory is an empty library, not an error: the KB is
// optional.
func Open(dir string) (*Library, error) {
	return open(dir, false)
}

func open(dir string, strict bool) (*Library, error) {
	l := &Library{dir: filepath.Join(dir, LocalDir)}
	for _, layer := range []struct{ sub, origin string }{
		{BuiltinDir, OriginBuiltin},
		{LocalDir, OriginInstance},
	} {
		if err := l.load(filepath.Join(dir, layer.sub), layer.origin, strict); err != nil {
			return nil, err
		}
	}
	// Dedupe by id, keeping the later (local) entry in the earlier slot.
	builtin := make(map[string]bool, len(l.entries))
	for _, e := range l.entries {
		if e.origin == OriginBuiltin {
			builtin[e.ID] = true
		}
	}
	seen := make(map[string]int, len(l.entries))
	merged := l.entries[:0]
	for _, e := range l.entries {
		if i, ok := seen[e.ID]; ok {
			merged[i] = e
			continue
		}
		seen[e.ID] = len(merged)
		merged = append(merged, e)
	}
	for i := range merged {
		if merged[i].origin == OriginInstance {
			merged[i].covers = builtin[merged[i].ID]
		}
	}
	l.builtin = builtin
	l.entries = merged
	sort.SliceStable(l.entries, func(i, j int) bool { return l.entries[i].ID < l.entries[j].ID })
	return l, nil
}

// load appends every entry under one directory, tagging it with the layer it
// came from. Unreadable files and entries without a title are skipped: a broken
// local file must not take the library down.
func (l *Library) load(dir, origin string, strict bool) error {
	seen := map[string]bool{}
	items, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, item := range items {
		if item.IsDir() || !strings.HasSuffix(strings.ToLower(item.Name()), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, item.Name()))
		if err != nil {
			if strict {
				return err
			}
			continue
		}
		var e Entry
		if json.Unmarshal(b, &e) != nil || (!e.Deleted && strings.TrimSpace(e.Title) == "") {
			if strict {
				return fmt.Errorf("KB 文件无效：%s", filepath.Join(dir, item.Name()))
			}
			continue
		}
		if e.ID == "" {
			e.ID = strings.TrimSuffix(item.Name(), filepath.Ext(item.Name()))
		}
		if strict && (!validID(e.ID) || seen[e.ID]) {
			return fmt.Errorf("KB ID 无效或同层重复：%s", e.ID)
		}
		seen[e.ID] = true
		e.origin = origin
		l.entries = append(l.entries, e)
	}
	return nil
}

// Search scores entries by query and tags. Query matching is deliberately
// simple and local: title 5 / description 3 / tags 4 / detail 1 per hit, tags
// filter (all must match) and add 2 each. An empty query lists the first N.
func (l *Library) Search(query string, tags []string, limit int) []Entry {
	if l == nil {
		return nil
	}
	if limit <= 0 {
		limit = 5
	}
	if limit > 20 {
		limit = 20
	}
	q := strings.ToLower(strings.TrimSpace(query))
	wantTags := make([]string, 0, len(tags))
	for _, t := range tags {
		if t = strings.ToLower(strings.TrimSpace(t)); t != "" {
			wantTags = append(wantTags, t)
		}
	}
	type hit struct {
		e     Entry
		score int
	}
	var hits []hit
	for _, e := range l.Entries() {
		if !hasTags(e, wantTags) {
			continue
		}
		score := 0
		if q != "" {
			if strings.Contains(strings.ToLower(e.Title), q) {
				score += 5
			}
			if strings.Contains(strings.ToLower(e.Description), q) {
				score += 3
			}
			if strings.Contains(strings.ToLower(e.Detail+" "+e.Applicability), q) {
				score++
			}
			for _, t := range e.Tags {
				if strings.Contains(strings.ToLower(t), q) {
					score += 4
				}
			}
			if score == 0 {
				continue
			}
		}
		score += 2 * len(wantTags)
		hits = append(hits, hit{e: e, score: score})
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].score != hits[j].score {
			return hits[i].score > hits[j].score
		}
		return hits[i].e.ID < hits[j].e.ID
	})
	if len(hits) > limit {
		hits = hits[:limit]
	}
	out := make([]Entry, 0, len(hits))
	for _, h := range hits {
		out = append(out, h.e)
	}
	return out
}

func hasTags(e Entry, want []string) bool {
	for _, w := range want {
		found := false
		for _, t := range e.Tags {
			if strings.EqualFold(t, w) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// Format renders a compact text result for the decider's tool output. The model
// needs the commands and the detail, not a JSON envelope; keep it tight.
func Format(entries []Entry) string {
	if len(entries) == 0 {
		return "（没有匹配的 KB 条目）"
	}
	var b strings.Builder
	for _, e := range entries {
		fmt.Fprintf(&b, "## %s %s\n%s\n", e.ID, e.Title, e.Description)
		if e.Detail != "" {
			b.WriteString(e.Detail + "\n")
		}
		if e.Applicability != "" {
			fmt.Fprintf(&b, "适用条件：%s\n", e.Applicability)
		}
		if e.Source != "" {
			fmt.Fprintf(&b, "来源：%s\n", e.Source)
		}
		for _, ref := range e.SourceRefs {
			fmt.Fprintf(&b, "引用：%s\n", ref)
		}
		if len(e.Commands) > 0 {
			b.WriteString("命令：\n")
			for _, c := range e.Commands {
				b.WriteString("- " + c + "\n")
			}
		}
		if len(e.Tags) > 0 {
			b.WriteString("tags: " + strings.Join(e.Tags, ", ") + "\n")
		}
		b.WriteString("\n")
	}
	return strings.TrimSpace(b.String())
}
