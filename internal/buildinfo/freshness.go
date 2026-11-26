package buildinfo

// Embedded-artifact freshness around a frontend build. This is the exact logic
// cmd/saker-gate uses, factored out so a unit test can prove both outcomes:
//
//   - a rebuild that produces the bytes the commit already has is not a failure,
//     even when the working tree was dirty before the gate started;
//   - a rebuild that produces a new difference means the committed embedded
//     product was behind the frontend sources, and the gate must fail.
//
// The comparison is content-based (tree snapshot digests), not mtime- or
// git-status-based: `vite build` rewrites every file, and a /mnt/c checkout
// reports phantom mode changes.

import (
	"fmt"
	"path/filepath"
)

// EmbeddedDir is the committed frontend build product consumed by go:embed.
const EmbeddedDir = "internal/web/static"

// CheckEmbeddedFreshness snapshots EmbeddedDir, calls build, snapshots again and
// reports the paths the rebuild changed. The build error is returned as-is; the
// comparison only happens after a successful build.
func CheckEmbeddedFreshness(root string, build func() error) (*EmbeddedArtifacts, error) {
	dir := filepath.Join(root, filepath.FromSlash(EmbeddedDir))
	before, err := SnapshotTree(dir)
	if err != nil {
		return nil, fmt.Errorf("快照 %s：%w", EmbeddedDir, err)
	}
	beforeStatus := gitStatusOrNote(root, EmbeddedDir)
	if err := build(); err != nil {
		return nil, err
	}
	after, err := SnapshotTree(dir)
	if err != nil {
		return nil, fmt.Errorf("快照 %s：%w", EmbeddedDir, err)
	}
	artifacts := &EmbeddedArtifacts{
		Dir:          EmbeddedDir,
		BeforeStatus: beforeStatus,
		AfterStatus:  gitStatusOrNote(root, EmbeddedDir),
		BeforeSHA256: before.AggregateSHA256(),
		AfterSHA256:  after.AggregateSHA256(),
		Changed:      DiffTreeSnapshots(before, after),
	}
	return artifacts, nil
}

// gitStatusOrNote records the pathspec status for the manifest but never fails
// the check: the content digest is the authoritative comparison.
func gitStatusOrNote(root, pathspec string) []string {
	state, err := GitStatusPaths(root, pathspec)
	if err != nil {
		return []string{"git status 失败：" + err.Error()}
	}
	return state
}
