package api

import (
	"strings"
	"testing"
	"time"

	"github.com/kksty/saker/internal/model"
	"github.com/kksty/saker/internal/store"
)

// The next brief must locate the previous batch that did not commit. This is the
// executor-side half of the same structured settlement the console shows; it is
// bounded and appears only for the direction's own previous run.
func TestBuildPromptLocatesPreviousIncompleteIngest(t *testing.T) {
	db := store.DB{
		Intents: []model.Intent{{ID: "in_1", Title: "继续", RunID: "r_prev"}},
		Runs: []model.Run{{
			ID: "r_prev", IntentID: "in_1", Status: model.RunDone, StartedAt: time.Now(),
			Ingest: &model.IngestOutcome{
				Status: model.IngestPartial, Declared: 2, Facts: 2, PersistedFacts: 1,
				CursorBefore: 120, CursorAfter: 120,
				Pending: []model.PendingDeclaration{{
					DeclarationID: "decl_b", Title: "第二句",
					Location: ".saker/facts.jsonl#bytes=120-180",
					Reasons:  []string{"落事实失败：fixture"},
				}},
			},
		}},
	}
	p := BuildPrompt(db, db.Intents[0], "", "", "", 0, false)
	if !strings.Contains(p, "## 上一轮未完成入账") {
		t.Fatalf("missing ingest section:\n%s", p)
	}
	if !strings.Contains(p, "decl_b") || !strings.Contains(p, ".saker/facts.jsonl#bytes=120-180") {
		t.Fatalf("declaration is not locatable:\n%s", p)
	}
	if !strings.Contains(p, "不重跑执行者") {
		t.Fatalf("the repair boundary is not stated:\n%s", p)
	}
	// A settled run stays quiet, and a first dispatch cannot inherit another
	// direction's pending batch.
	db.Runs[0].Ingest = &model.IngestOutcome{Status: model.IngestSettled, Committed: true}
	if p := BuildPrompt(db, db.Intents[0], "", "", "", 0, false); strings.Contains(p, "## 上一轮未完成入账") {
		t.Fatalf("settled run produced an ingest section:\n%s", p)
	}
	first := model.Intent{ID: "in_new", Title: "首次"}
	if p := BuildPrompt(db, first, "", "", "", 0, false); strings.Contains(p, "## 上一轮未完成入账") {
		t.Fatalf("first dispatch inherited another run:\n%s", p)
	}
}
