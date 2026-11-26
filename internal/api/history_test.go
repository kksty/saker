package api

import (
	"strings"
	"testing"
	"time"

	"github.com/kksty/saker/internal/model"
	"github.com/kksty/saker/internal/store"
)

// The history index exists because a successor used to get one line ("last run
// ended like this") and no pointer to the earlier attempts' journals, so the
// same probes and credential tests were paid for again. These assertions pin
// the three properties the index must keep: it covers every settled run of this
// direction (not just the predecessor), it never lists the running dispatch or
// another direction's runs, and it is an index — read path and end reason, no
// retry instruction.
func historyFixture() store.DB {
	base := time.Date(2026, 11, 24, 21, 45, 0, 0, time.Local)
	end := base.Add(31 * time.Minute)
	earlier := base.Add(-2 * time.Hour)
	earlierEnd := earlier.Add(20 * time.Minute)
	return store.DB{
		Intents: []model.Intent{{ID: "in_x", Title: "重试", RunID: "r_prev", Attempts: 3}},
		Runs: []model.Run{
			{ID: "r_prev", IntentID: "in_x", Status: model.RunCanceled, CancelCause: model.CancelCausePause, StartedAt: base, EndedAt: &end, DeclaredFacts: 2},
			{ID: "r_old", IntentID: "in_x", Status: model.RunFailed, StopReason: "eval_error", StartedAt: earlier, EndedAt: &earlierEnd},
			{ID: "r_live", IntentID: "in_x", Status: model.RunRunning, StartedAt: end},
			{ID: "r_other", IntentID: "in_y", Status: model.RunFailed, StartedAt: earlier},
		},
	}
}

func TestOwnHistoryIndexesEverySettledRunOfThisDirection(t *testing.T) {
	db := historyFixture()
	p := BuildPrompt(db, db.Intents[0], "", "", "", 0, false)

	for _, want := range []string{"## 本意图历史", "第 3 次派发", "`r_prev`", "`r_old`", "项目暂停", "$SAKER_BATTLE/runs/<run_id>/events"} {
		if !strings.Contains(p, want) {
			t.Fatalf("prompt missing %q:\n%s", want, p)
		}
	}
	if strings.Contains(p, "`r_live`") {
		t.Fatalf("history listed the running dispatch as history:\n%s", p)
	}
	if strings.Contains(p, "`r_other`") {
		t.Fatalf("history leaked another direction's run:\n%s", p)
	}
	if strings.Index(p, "`r_prev`") > strings.Index(p, "`r_old`") {
		t.Fatalf("history is not newest-first:\n%s", p)
	}
	// The path is offered, never opened on the model's behalf: the index must
	// not claim what the earlier runs tried.
	if !strings.Contains(p, "不读不会自动带进来") {
		t.Fatalf("history does not mark the journal as opt-in:\n%s", p)
	}
}

// An operator's hold is a decision, not a failure: the successor must be able to
// tell it apart from a pause or a crash before it tries to restart the same
// direction through another entry point (the hold refusal returns 409).
func TestOwnHistoryNamesTheStopperForOperatorCancel(t *testing.T) {
	db := historyFixture()
	db.Runs[0].CancelCause = model.CancelCauseOperator
	p := BuildPrompt(db, db.Intents[0], "", "", "", 0, false)
	if !strings.Contains(p, "操作员停止（hold，不可换入口重试）") {
		t.Fatalf("operator hold not named in history:\n%s", p)
	}
}

func TestOwnHistoryIsAnIndexNotARestartHandle(t *testing.T) {
	db := historyFixture()
	p := BuildPrompt(db, db.Intents[0], "", "", "", 0, false)
	for _, unwanted := range []string{"/restart", "workers/", "建议重试", "应当继续"} {
		if strings.Contains(p, unwanted) {
			t.Fatalf("history carries an action or advice (%q):\n%s", unwanted, p)
		}
	}
	if !strings.Contains(p, "以上只是历史记录，不代表本轮必须怎么做") {
		t.Fatalf("history does not mark itself as non-prescriptive:\n%s", p)
	}
}

func TestOwnHistoryIsBoundedAndSaysHowManyAreHidden(t *testing.T) {
	base := time.Date(2026, 11, 24, 12, 0, 0, 0, time.Local)
	db := store.DB{Intents: []model.Intent{{ID: "in_x", RunID: "r_0", Attempts: 9}}}
	for i := 0; i < 9; i++ {
		end := base.Add(time.Duration(i+1) * time.Minute)
		db.Runs = append(db.Runs, model.Run{
			ID: "r_" + string(rune('0'+i)), IntentID: "in_x", Status: model.RunCanceled,
			StartedAt: base.Add(time.Duration(i) * time.Minute), EndedAt: &end,
		})
	}
	p := BuildPrompt(db, db.Intents[0], "", "", "", 0, false)
	if got := strings.Count(p, "；交接事实 "); got != historyRunMax {
		t.Fatalf("history lists %d runs, want %d:\n%s", got, historyRunMax, p)
	}
	if !strings.Contains(p, "更早 3 次未列出") {
		t.Fatalf("history does not say how many runs it hid:\n%s", p)
	}
}
