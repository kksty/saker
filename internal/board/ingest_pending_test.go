package board

import (
	"testing"
	"time"

	"github.com/kksty/saker/internal/model"
	"github.com/kksty/saker/internal/store"
)

// The board is the commander's and operator's shared glance: an incomplete
// delivery must be visible there even when the run itself ended cleanly, with
// the declaration id and byte span that a repair needs.
func TestBoardExposesIncompleteIngestSeparatelyFromRunStatus(t *testing.T) {
	db := store.DB{
		Intents: []model.Intent{{ID: "in_1", Title: "方向", Status: model.StatusDone}},
		Runs: []model.Run{{
			ID: "r_1", IntentID: "in_1", Status: model.RunDone, StartedAt: time.Now(),
			Ingest: &model.IngestOutcome{
				Status: model.IngestPartial, Declared: 2, Facts: 2, PersistedFacts: 1,
				CursorBefore: 100, CursorAfter: 100,
				Pending: []model.PendingDeclaration{{
					DeclarationID: "decl_b", Kind: "note", Title: "第二句",
					Location: ".saker/facts.jsonl#bytes=120-180",
					Reasons:  []string{"落事实失败：fixture"},
				}},
				Reasons: []string{"落事实失败：fixture"},
			},
		}},
	}
	v := BuildFromDB(db, 20)
	if len(v.IngestPending) != 1 {
		t.Fatalf("ingest pending = %+v", v.IngestPending)
	}
	row := v.IngestPending[0]
	if row.Run != "r_1" || row.IntentID != "in_1" || row.Status != model.IngestPartial ||
		row.Persisted != 1 || row.Declared != 2 || len(row.Pending) != 1 {
		t.Fatalf("row = %+v", row)
	}
	if loc := row.Pending[0].Location; loc != ".saker/facts.jsonl#bytes=120-180" {
		t.Fatalf("location = %q", loc)
	}
	// A settled run does not appear, so "done" is not mistaken for "still owed".
	db.Runs[0].Ingest = &model.IngestOutcome{Status: model.IngestSettled, Committed: true}
	if got := BuildFromDB(db, 20).IngestPending; len(got) != 0 {
		t.Fatalf("settled run appeared as pending: %+v", got)
	}
}
