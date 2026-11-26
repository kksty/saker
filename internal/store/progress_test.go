package store

import (
	"path/filepath"
	"testing"

	"github.com/kksty/saker/internal/model"
)

func openProgressStore(t *testing.T, path string) *Store {
	t.Helper()
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// A dry direction gets exactly one alert per episode. The durable flag is what
// stops the round after the threshold (and a restart) from repeating it.
func TestObserveDecisionRoundEscalatesOncePerDryEpisode(t *testing.T) {
	st := openProgressStore(t, filepath.Join(t.TempDir(), "ledger.json"))
	defer st.Close()

	for i := 1; i <= 3; i++ {
		obs, err := st.ObserveDecisionRound(3)
		if err != nil {
			t.Fatal(err)
		}
		if obs.Rounds != i {
			t.Fatalf("round %d: count = %d, want %d", i, obs.Rounds, i)
		}
		if want := i == 3; obs.Escalate != want {
			t.Fatalf("round %d: escalate = %v, want %v", i, obs.Escalate, want)
		}
	}
	next, err := st.ObserveDecisionRound(3)
	if err != nil {
		t.Fatal(err)
	}
	if next.Rounds != 4 || next.Escalate {
		t.Fatalf("round after the alert = %+v, want count 4 and no second alert", next)
	}
	if got := st.StallRounds(); got != 4 {
		t.Fatalf("stored count = %d, want 4", got)
	}
}

// 用例②: any progress in between zeroes the count, and a later episode must
// earn the full limit again. Removing the reset makes this test escalate at
// round 3 instead of 0/2.
func TestProgressResetsStallCounter(t *testing.T) {
	st := openProgressStore(t, filepath.Join(t.TempDir(), "ledger.json"))
	defer st.Close()

	for i := 1; i <= 2; i++ {
		if _, err := st.ObserveDecisionRound(3); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.AddFact(model.Fact{Title: "新的侦察结果"}); err != nil {
		t.Fatal(err)
	}
	obs, err := st.ObserveDecisionRound(3)
	if err != nil {
		t.Fatal(err)
	}
	if obs.Rounds != 0 || obs.Escalate || obs.NewFacts != 1 {
		t.Fatalf("round with a new fact = %+v, want count 0 and no alert", obs)
	}
	// The new episode starts here: two dry rounds are not enough.
	for i := 1; i <= 2; i++ {
		obs, err = st.ObserveDecisionRound(3)
		if err != nil {
			t.Fatal(err)
		}
		if obs.Rounds != i || obs.Escalate {
			t.Fatalf("new episode round %d = %+v", i, obs)
		}
	}
	obs, err = st.ObserveDecisionRound(3)
	if err != nil {
		t.Fatal(err)
	}
	if obs.Rounds != 3 || !obs.Escalate {
		t.Fatalf("third dry round of the new episode = %+v, want count 3 and an alert", obs)
	}
}

// A closed direction is progress too. Only the status transition counts: a
// later edit of an already-settled direction must not fake a second round.
func TestClosedIntentCountsAsProgress(t *testing.T) {
	st := openProgressStore(t, filepath.Join(t.TempDir(), "ledger.json"))
	defer st.Close()

	in, err := st.CreateIntent(model.Intent{Title: "待关闭的意图"})
	if err != nil {
		t.Fatal(err)
	}
	obs, err := st.ObserveDecisionRound(3)
	if err != nil {
		t.Fatal(err)
	}
	if obs.Rounds != 1 || obs.NewIntents != 1 || obs.Opened != 1 {
		t.Fatalf("new direction = %+v, want a counted dry round (opening is not progress)", obs)
	}
	if _, err := st.UpdateIntent(in.ID, func(i *model.Intent) { i.Status = model.StatusDone }); err != nil {
		t.Fatal(err)
	}
	obs, err = st.ObserveDecisionRound(3)
	if err != nil {
		t.Fatal(err)
	}
	if obs.Rounds != 0 || obs.ClosedIntents != 1 || obs.Opened != 0 {
		t.Fatalf("closed direction = %+v, want progress on the close and a reset", obs)
	}
	// Editing a finished direction (not its status) is bookkeeping, not progress.
	if _, err := st.UpdateIntent(in.ID, func(i *model.Intent) { i.Description = "结果已归档" }); err != nil {
		t.Fatal(err)
	}
	obs, err = st.ObserveDecisionRound(3)
	if err != nil {
		t.Fatal(err)
	}
	if obs.Rounds != 1 || obs.Escalate {
		t.Fatalf("edit of a settled direction counted as progress: %+v", obs)
	}
}

// Opening directions is not progress. It is still counted per episode so the
// alert can report "opened N directions, zero new facts". Counting creates as
// progress was what let those dry stretches pass unnoticed in the live ledger.
func TestNewDirectionIsNotProgress(t *testing.T) {
	st := openProgressStore(t, filepath.Join(t.TempDir(), "ledger.json"))
	defer st.Close()

	for i := 1; i <= 3; i++ {
		if _, err := st.CreateIntent(model.Intent{Title: "拆出来的意图"}); err != nil {
			t.Fatal(err)
		}
		obs, err := st.ObserveDecisionRound(3)
		if err != nil {
			t.Fatal(err)
		}
		if obs.Rounds != i || obs.Opened != i || obs.NewFacts != 0 {
			t.Fatalf("round %d with only a new direction = %+v", i, obs)
		}
		if want := i == 3; obs.Escalate != want {
			t.Fatalf("round %d escalate = %v, want %v", i, obs.Escalate, want)
		}
	}
	// One alert per episode, even while the directions keep coming.
	if _, err := st.CreateIntent(model.Intent{Title: "再拆一条"}); err != nil {
		t.Fatal(err)
	}
	obs, err := st.ObserveDecisionRound(3)
	if err != nil {
		t.Fatal(err)
	}
	if obs.Rounds != 4 || obs.Opened != 4 || obs.Escalate {
		t.Fatalf("round after the alert = %+v, want count 4, four directions, no repeat", obs)
	}
}

// The counter is ledger memory: a restart continues the same dry episode
// instead of hiding it behind a fresh zero.
func TestStallCounterSurvivesReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ledger.json")
	st := openProgressStore(t, path)
	for i := 0; i < 2; i++ {
		if _, err := st.CreateIntent(model.Intent{Title: "重启前拆的意图"}); err != nil {
			t.Fatal(err)
		}
		if _, err := st.ObserveDecisionRound(3); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	reopened := openProgressStore(t, path)
	defer reopened.Close()
	if got := reopened.StallRounds(); got != 2 {
		t.Fatalf("count after reopen = %d, want 2", got)
	}
	if _, err := reopened.CreateIntent(model.Intent{Title: "重启后拆的意图"}); err != nil {
		t.Fatal(err)
	}
	obs, err := reopened.ObserveDecisionRound(3)
	if err != nil {
		t.Fatal(err)
	}
	if obs.Rounds != 3 || obs.Opened != 3 || !obs.Escalate {
		t.Fatalf("first round after reopen = %+v, want the episode (and its direction count) to continue", obs)
	}
}

// Parking, failure and review failure in the sixth dry round must not suppress
// its alert.
func TestStallCountsIntentTransitionsWithoutReset(t *testing.T) {
	st := openProgressStore(t, filepath.Join(t.TempDir(), "ledger.json"))
	defer st.Close()
	for i := 0; i < 5; i++ {
		if _, err := st.ObserveDecisionRound(6); err != nil {
			t.Fatal(err)
		}
	}
	for _, status := range []string{model.StatusFailed, model.StatusReviewFailed, model.StatusParked} {
		in, err := st.CreateIntent(model.Intent{Title: status})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.UpdateIntent(in.ID, func(in *model.Intent) { in.Status = status }); err != nil {
			t.Fatal(err)
		}
	}
	obs, err := st.ObserveDecisionRound(6)
	if err != nil {
		t.Fatal(err)
	}
	if obs.Rounds != 6 || !obs.Escalate || obs.Opened != 3 || obs.Parked != 1 || obs.Failed != 1 || obs.ReviewFailed != 1 {
		t.Fatalf("sixth round with intent changes: %+v", obs)
	}
}

// Only the first valid receipt for the assertion that is current at the
// checkpoint is progress. A repeat verdict or proof superseded before the
// checkpoint must not erase a dry episode.
func TestStallResetsOnFirstCurrentProof(t *testing.T) {
	st := openProgressStore(t, filepath.Join(t.TempDir(), "ledger.json"))
	defer st.Close()
	e, err := st.AddEvidence(model.Evidence{URI: "fixture/result.txt", Name: "result.txt"})
	if err != nil {
		t.Fatal(err)
	}
	f, err := st.AddFact(model.Fact{Kind: "artifact", Origin: model.OriginDeclared, Title: "fixture", EvidenceIDs: []string{e.ID}})
	if err != nil {
		t.Fatal(err)
	}
	observe := func() RoundProgress {
		t.Helper()
		got, err := st.ObserveDecisionRound(6)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	review := func(verifier string) {
		t.Helper()
		if _, err := st.ReviewFact(model.Review{FactID: f.ID, Verdict: "pass", VerifierRun: verifier}); err != nil {
			t.Fatal(err)
		}
	}
	rewrite := func(summary string) {
		t.Helper()
		if _, err := st.UpdateFact(f.ID, func(f *model.Fact) { f.Summary = summary }); err != nil {
			t.Fatal(err)
		}
	}
	observe() // Consume the new fact.
	for i := 0; i < 5; i++ {
		observe()
	}
	review("verifier-v1")
	rewrite("v2")
	if obs := observe(); obs.NewProofs != 0 || !obs.Escalate {
		t.Fatalf("superseded proof counted: %+v", obs)
	}
	review("verifier-v2")
	if obs := observe(); obs.NewProofs != 1 || obs.Rounds != 0 || obs.Escalate {
		t.Fatalf("first current proof did not reset: %+v", obs)
	}
	for i := 0; i < 5; i++ {
		observe()
	}
	review("verifier-repeat")
	if obs := observe(); obs.NewProofs != 0 || !obs.Escalate {
		t.Fatalf("repeated proof counted: %+v", obs)
	}
}
