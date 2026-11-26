package buildinfo

// Embedded-artifact freshness. The committed internal/web/static is a build
// product: when a frontend source changes and the product is not rebuilt, Go
// silently embeds the old UI. The gate snapshots the directory before the
// frontend build and after it; a content difference means this build produced
// bytes the commit does not have, i.e. the embedded product was stale.
//
// The snapshot deliberately ignores mtime and git metadata: `vite build` rewrites
// unchanged files, and a /mnt/c checkout reports phantom mode changes. Only the
// path set and content digests matter.

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// FileDigest is one file's identity inside a tree snapshot.
type FileDigest struct {
	Kind   string `json:"kind"` // "file" or "symlink"
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// TreeSnapshot is a directory's file set and content digests. Paths are
// slash-separated and relative, so Windows and Linux snapshots compare directly.
type TreeSnapshot struct {
	// Exists is false when the directory was absent. An absent tree is a valid
	// snapshot (empty); the gate reports it as a fact rather than crashing.
	Exists bool                  `json:"exists"`
	Files  map[string]FileDigest `json:"files"`
}

// SnapshotTree walks dir and records every regular file and symlink. A missing
// directory is an empty snapshot; an unreadable one is an error, because silently
// skipping a file would hide exactly the stale-artifact failure this guards.
func SnapshotTree(dir string) (*TreeSnapshot, error) {
	snap := &TreeSnapshot{Files: map[string]FileDigest{}}
	info, err := os.Stat(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return snap, nil
		}
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%s 不是目录", dir)
	}
	snap.Exists = true
	err = filepath.WalkDir(dir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		size, sum, kind, err := hashFile(path)
		if err != nil {
			return fmt.Errorf("摘要 %s：%w", path, err)
		}
		snap.Files[filepath.ToSlash(rel)] = FileDigest{Kind: kind, Size: size, SHA256: sum}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return snap, nil
}

// AggregateSHA256 is one stable digest of the whole snapshot.
func (s *TreeSnapshot) AggregateSHA256() string {
	if s == nil {
		return ""
	}
	h := sha256.New()
	_, _ = io.WriteString(h, "saker-tree-v1\n")
	for _, path := range s.Paths() {
		f := s.Files[path]
		fmt.Fprintf(h, "%s\x00%s\x00%s\x00%d\n", path, f.Kind, f.SHA256, f.Size)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Paths lists the snapshot's file paths in sorted order.
func (s *TreeSnapshot) Paths() []string {
	if s == nil {
		return nil
	}
	paths := make([]string, 0, len(s.Files))
	for p := range s.Files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	return paths
}

// DiffTreeSnapshots returns the paths whose content, kind or presence changed
// between two snapshots. A sorted, deterministic list keeps the gate message and
// the manifest stable across runs.
func DiffTreeSnapshots(before, after *TreeSnapshot) []string {
	seen := map[string]struct{}{}
	for p := range before.Files {
		seen[p] = struct{}{}
	}
	for p := range after.Files {
		seen[p] = struct{}{}
	}
	changed := make([]string, 0)
	for p := range seen {
		b, inBefore := before.Files[p]
		a, inAfter := after.Files[p]
		if inBefore != inAfter || b != a {
			changed = append(changed, p)
		}
	}
	sort.Strings(changed)
	return changed
}

// FormatChangedPaths bounds a change list for a human-readable error message.
func FormatChangedPaths(changed []string, limit int) string {
	if len(changed) == 0 {
		return ""
	}
	shown := changed
	if limit > 0 && len(shown) > limit {
		shown = shown[:limit]
	}
	out := strings.Join(shown, ", ")
	if len(changed) > len(shown) {
		out += fmt.Sprintf(" 等 %d 个文件", len(changed))
	}
	return out
}
