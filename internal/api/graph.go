package api

import (
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"

	"github.com/kksty/saker/internal/board"
	"github.com/kksty/saker/internal/model"
	"github.com/kksty/saker/internal/store"
)

// graphFile is the executor's read-only snapshot of the ledger. It lives inside
// .saker, which never travels in a bundle: it is a working view, not a deliverable.
const graphFile = ".saker/graph.json"

// writeGraph writes the snapshot and reports whether the prompt may point at
// it. A failed write is not a failed dispatch — the run still has the brief,
// and the brief must not promise a file that is not there.
func writeGraph(workDir string, db store.DB, scope string, focus ...string) bool {
	return writeGraphFiles(workDir, "", db, scope, focus...) == nil
}

func writeGraphFiles(workDir, prefix string, db store.DB, scope string, focus ...string) error {
	// The graph is a decision surface: an executor reads it and acts. Only a
	// verified assertion keeps its text; an unverified observation the model can
	// read is one it will use without ever citing, which no review trigger
	// catches.
	projected := decisionDB(db)
	var g board.Graph
	if len(focus) > 0 {
		g = board.BuildTaskGraph(projected, scope, focus[0])
		// The catalog uses only IDs, versions and states. It must look up
		// those states in the same ledger that supplied the graph.
		g.Context.Manifest = path.Join(prefix, ".saker/context.json")
		if err := writeTaskManifest(workDir, prefix, db, focus[0]); err != nil {
			return err
		}
	} else {
		g = board.BuildGraph(projected, scope)
	}
	target := filepath.Join(workDir, filepath.FromSlash(graphFile))
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	return writeJSONFileAtomic(target, g)
}

// decisionDB keeps only verified facts in the ledger copy a decision surface is
// built from. The facts still exist in the ledger and stay readable through the
// fact API with their status; they just do not ride a surface the model reads by
// default.
func decisionDB(db store.DB) store.DB {
	kept := make([]model.Fact, 0, len(db.Facts))
	for _, f := range db.Facts {
		if f.Status != "verified" {
			continue
		}
		kept = append(kept, f)
	}
	db.Facts = kept
	return db
}

// Premise states in the task manifest. The manifest is a decision surface, so
// these words are the run's whole answer to "is this premise still usable?":
//
//	recorded        — verified at its current claim; usable as a premise
//	                  (a registered original that cannot be opened is a recorded
//	                  gap, not a refusal — dispatch logs dispatch_gap)
//	unverified      — reviewable, but no passing review covers this version
//	unreceipted     — verified, but no passing review is bound to this
//	                  assertion: the receipt proves an older one
//	reference_only  — a kind no review can ever pass (notes, raw material);
//	                  readable, but never admissible as a necessary premise
//	withdrawn       — taken out of the usable surface by its author
//	rejected        — a review found it false
//	missing         — named as a premise, absent from the ledger
//
// Adding a synonym here (pending, stale, …) breaks the one contract the run
// reads, so extend this list instead of inventing a word at the write site.
const (
	premiseRecorded      = "recorded"
	premiseUnverified    = "unverified"
	premiseUnreceipted   = "unreceipted"
	premiseReferenceOnly = "reference_only"
	premiseWithdrawn     = "withdrawn"
	premiseRejected      = "rejected"
	premiseMissing       = "missing"
)

func writeTaskManifest(workDir, prefix string, db store.DB, intentID string) error {
	type ref struct {
		ID      string `json:"id"`
		Version uint64 `json:"version"`
		State   string `json:"state"`
	}
	byID := map[string]model.Fact{}
	versions := map[string]uint64{}
	for _, f := range db.Facts {
		byID[f.ID] = f
	}
	if db.Memory != nil {
		for _, c := range db.Memory.History {
			if c.Kind == "fact" {
				versions[c.ID] = c.Version
			}
		}
	}
	refs := []ref{}
	for _, in := range db.Intents {
		if in.ID == intentID {
			for _, id := range in.FromFactIDs {
				state := premiseRecorded
				f, ok := byID[id]
				if !ok {
					state = premiseMissing
				} else if f.Status == "withdrawn" {
					state = premiseWithdrawn
				} else if f.Status == "rejected" {
					state = premiseRejected
				} else if !model.ReviewableKind(f.Kind) {
					// A kind no review can ever pass never earns a verdict. A row
					// of that kind carrying one comes from an imported or
					// repaired ledger, and admission still treats it as
					// reference material only.
					state = premiseReferenceOnly
				} else if f.Status != "verified" {
					// "recorded" means "in the ledger and usable". A row that is
					// still unverified must not read as usable here: the catalog
					// is a decision surface, and this is the only place the run
					// learns which of its premises are still backed.
					state = premiseUnverified
				} else if _, proven := model.CurrentProof(f, db.Reviews); !proven {
					// Verified with an original in place, yet no pass is bound to the
					// assertion it carries now: a ledger written by an older build or
					// imported from one looks proven while admission refuses it for the
					// missing receipt: the same gap one step later in the rule.
					state = premiseUnreceipted
				}
				refs = append(refs, ref{id, versions[id], state})
			}
		}
	}
	dir := filepath.Join(workDir, ".saker", "context")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	pages := []string{}
	for start := 0; start < len(refs); start += 128 {
		end := start + 128
		if end > len(refs) {
			end = len(refs)
		}
		name := fmt.Sprintf("premises-%04d.json", len(pages)+1)
		raw, err := json.Marshal(refs[start:end])
		if err != nil {
			return err
		}
		if err = os.WriteFile(filepath.Join(dir, name), raw, 0600); err != nil {
			return err
		}
		pages = append(pages, path.Join(prefix, ".saker/context", name))
	}
	// A later dispatch can need fewer pages than an earlier one; without this
	// sweep the directory keeps higher-numbered pages that the new manifest
	// does not name, and a reader following the old names finds stale premises.
	keep := map[string]bool{}
	for _, rel := range pages {
		keep[filepath.Base(rel)] = true
	}
	if err := pruneStalePages(dir, "premises-*.json", keep); err != nil {
		return err
	}
	manifest := map[string]any{"intent_id": intentID, "prerequisites_total": len(refs), "pages": pages, "graph_query": "/api/graph?root=intent:" + intentID + "&direction=ancestors&depth=0", "fact_query": "/api/facts?intent=" + intentID, "snapshot": true}
	if db.Memory != nil {
		manifest["epoch"] = db.Memory.Epoch
		manifest["seq"] = db.Memory.Seq
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(workDir, ".saker", "context.json"), raw, 0600)
}