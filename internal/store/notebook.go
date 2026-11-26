package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/kksty/saker/internal/model"
)

var ErrNoteConflict = errors.New("笔记版本或写入轮次已变化")

func NoteHash(text string) string {
	h := sha256.Sum256([]byte(text))
	return hex.EncodeToString(h[:])
}

func detachNotebook(n model.Notebook) model.Notebook {
	n.FactVersions = copyVersions(n.FactVersions)
	n.FactClaimVersions = copyVersions(n.FactClaimVersions)
	return n
}

func (s *Store) Notebook() (model.Notebook, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.db.Notebooks) == 0 {
		return model.Notebook{}, false
	}
	return detachNotebook(s.db.Notebooks[0]), true
}

func (s *Store) NotebookVersion(version uint64) (model.Notebook, bool) {
	for _, c := range s.History("notebook", "commander") {
		if c.Version == version {
			var n model.Notebook
			if json.Unmarshal(c.Value, &n) == nil {
				return n, true
			}
		}
	}
	return model.Notebook{}, false
}

func (s *Store) StaleNotebookFacts(n model.Notebook) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var stale []string
	for id, version := range n.FactVersions {
		r, ok := s.records[recordKey("fact", id)]
		if !ok || r.version != version || r.value.(model.Fact).Unusable() {
			stale = append(stale, id)
		}
	}
	sort.Strings(stale)
	return stale
}

// SaveNotebook commits bytes, provenance and the run receipt atomically. There
// is no action-log inference and no file/ledger dual-write recovery window.
func (s *Store) SaveNotebook(expected uint64, text, writer, runID string, refs map[string]uint64) (model.Notebook, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var current uint64
	if len(s.db.Notebooks) > 0 {
		current = s.db.Notebooks[0].Version
	}
	if current != expected {
		return model.Notebook{}, ErrNoteConflict
	}
	var run *model.Run
	if writer == "commander" {
		for i := range s.db.Runs {
			if s.db.Runs[i].ID == runID {
				run = &s.db.Runs[i]
				break
			}
		}
		if run == nil || run.Purpose != "commander" || run.Status != model.RunRunning {
			return model.Notebook{}, ErrNoteConflict
		}
	} else if writer != "operator" || runID != "" {
		return model.Notebook{}, fmt.Errorf("invalid note writer")
	}
	claims := map[string]uint64{}
	for id, version := range refs {
		r, ok := s.records[recordKey("fact", id)]
		if !ok || version == 0 || r.version != version {
			return model.Notebook{}, fmt.Errorf("%w：事实 %s", ErrNoteConflict, id)
		}
		claims[id] = r.value.(model.Fact).ClaimVersion
	}
	if writer == "operator" {
		for i := range s.db.Runs {
			active := &s.db.Runs[i]
			if active.Purpose == "commander" && active.Status == model.RunRunning {
				active.Infos = append(active.Infos, fmt.Sprintf("审计：指挥官轮次运行期间，操作员凭据改写笔记 v%d；此写入不作为指挥官交接回执", current+1))
			}
		}
	}
	n := model.Notebook{ID: "commander", State: text, WrittenAt: time.Now(),
		NoteRef:      model.NoteRef{Version: current + 1, SHA256: NoteHash(text), Writer: writer, RunID: runID},
		FactVersions: copyVersions(refs), FactClaimVersions: claims}
	if run != nil {
		n.Round = run.CommanderRound
		run.NoteWrites = append(append([]model.NoteRef(nil), run.NoteWrites...), n.NoteRef)
	}
	s.db.Notebooks = []model.Notebook{n}
	if err := s.flushNoRev("notebook", "run"); err != nil {
		return model.Notebook{}, err
	}
	return detachNotebook(n), nil
}

// ValidNoteHandoff requires the current preserved version to have been written
// by this successful round. Operator writes and another round's receipts cannot
// satisfy a handoff, even if the action summary contains a state mutation.
//
// A blank body is not a handoff either: writing "nothing" proves the same thing
// as not writing at all, and counting it as a receipt would close the pressure
// cycle (and arm a session rotation) on top of an empty memory. The note
// contract asks for conclusions, leads and the next step; only a written body
// can carry them.
func (s *Store) ValidNoteHandoff(runID string, after uint64) (*model.NoteRef, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.db.Notebooks) == 0 {
		return nil, false
	}
	n := s.db.Notebooks[0]
	if n.Version <= after || n.Writer != "commander" || n.RunID != runID || n.SHA256 != NoteHash(n.State) {
		return nil, false
	}
	if strings.TrimSpace(n.State) == "" {
		return nil, false
	}
	for _, run := range s.db.Runs {
		if run.ID != runID || run.Purpose != "commander" || run.Status != model.RunDone {
			continue
		}
		for _, ref := range run.NoteWrites {
			if ref == n.NoteRef {
				v := ref
				return &v, true
			}
		}
	}
	return nil, false
}
