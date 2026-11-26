package api

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"

	"github.com/kksty/saker/internal/store"
)

const runInputFile = ".saker/run-input.json"

type runInputManifest struct {
	RunID    string             `json:"run_id"`
	Snapshot *store.ReadVersion `json:"snapshot"`
	State    string             `json:"state"`
	Graph    string             `json:"graph,omitempty"`
	Focus    string             `json:"focus,omitempty"`
	Context  string             `json:"context,omitempty"`
}

// publishRunInput publishes one manifest after the entire directory is ready.
// Until then readers see an unavailable manifest for the new run.
func publishRunInput(workDir, runID string, db store.DB, scope, intentID string) error {
	if runID == "." || !filepath.IsLocal(runID) || filepath.Base(runID) != runID {
		return fmt.Errorf("invalid run input ID")
	}
	root := filepath.Join(workDir, ".saker", "inputs")
	if err := os.MkdirAll(root, 0o700); err != nil {
		return err
	}
	marker := filepath.Join(workDir, filepath.FromSlash(runInputFile))
	manifest := runInputManifest{RunID: runID, Snapshot: focusSnapshot(db), State: "unavailable"}
	if err := writeJSONFileAtomic(marker, manifest); err != nil {
		return err
	}
	stage, err := os.MkdirTemp(root, ".input-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	rel := path.Join(".saker/inputs", runID)
	if err := writeGraphFiles(stage, rel, db, scope, intentID); err != nil {
		return err
	}
	if err := writeFocusFilesAt(stage, rel, db, intentID); err != nil {
		return err
	}
	target := filepath.Join(root, runID)
	if _, err := os.Lstat(target); err == nil {
		return fmt.Errorf("run input already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Rename(stage, target); err != nil {
		return err
	}
	manifest.State = "ready"
	manifest.Graph = path.Join(rel, graphFile)
	manifest.Focus = path.Join(rel, focusFile)
	manifest.Context = path.Join(rel, ".saker/context.json")
	return writeJSONFileAtomic(marker, manifest)
}
