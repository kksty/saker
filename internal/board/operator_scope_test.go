package board

import (
	"testing"
	"time"

	"github.com/kksty/saker/internal/model"
	"github.com/kksty/saker/internal/store"
)

// The late annotation is a pure identity+time projection: only an explicit stop
// entry, an exactly listed entity_ref, and a fact recorded after the stop match.
// Nothing about the fact's title/summary/kind is read.
func TestLateFactsByFact(t *testing.T) {
	stopAt := time.Date(2026, 3, 1, 8, 0, 0, 0, time.UTC)
	stops := []store.OperatorScopeEntry{
		{ID: "os-auth", Kind: store.OperatorScopeKindAuthorization, At: stopAt.Add(-time.Hour), EntityRefs: []string{"host:a"}},
		{ID: "os-stop", Kind: store.OperatorScopeKindStop, At: stopAt, EntityRefs: []string{"host:a", "account:uid=2"}},
	}
	facts := []model.Fact{
		{ID: "f_after", EntityRef: "host:a", Title: "unrelated wording", CreatedAt: stopAt.Add(time.Minute)},
		{ID: "f_before", EntityRef: "host:a", CreatedAt: stopAt.Add(-time.Minute)},
		{ID: "f_other_entity", EntityRef: "host:b", CreatedAt: stopAt.Add(time.Minute)},
		{ID: "f_no_entity", Title: "host:a appears in prose", CreatedAt: stopAt.Add(time.Minute)},
		{ID: "f_late_account", EntityRef: "account:uid=2", CreatedAt: stopAt.Add(2 * time.Minute)},
	}
	got := LateFactsByFact(facts, stops)
	if len(got) != 2 {
		t.Fatalf("annotated = %+v, want exactly the two matching facts", got)
	}
	if got["f_after"].StopID != "os-stop" || !got["f_after"].At.Equal(stopAt) {
		t.Fatalf("f_after = %+v", got["f_after"])
	}
	if got["f_late_account"].StopID != "os-stop" {
		t.Fatalf("f_late_account = %+v", got["f_late_account"])
	}
	for _, id := range []string{"f_before", "f_other_entity", "f_no_entity"} {
		if _, ok := got[id]; ok {
			t.Fatalf("%s must not be annotated (time/identity rule): %+v", id, got[id])
		}
	}
}

// With several applicable stops the most recent one before the fact is the
// winner; the annotation is stable regardless of file order.
func TestLateFactsByFactUsesMostRecentApplicableStop(t *testing.T) {
	early := time.Date(2026, 3, 1, 8, 0, 0, 0, time.UTC)
	late := early.Add(time.Hour)
	fact := model.Fact{ID: "f_1", EntityRef: "host:a", CreatedAt: early.Add(2 * time.Hour)}
	forward := []store.OperatorScopeEntry{
		{ID: "os-early", Kind: store.OperatorScopeKindStop, At: early, EntityRefs: []string{"host:a"}},
		{ID: "os-late", Kind: store.OperatorScopeKindStop, At: late, EntityRefs: []string{"host:a"}},
	}
	reversed := []store.OperatorScopeEntry{forward[1], forward[0]}
	for _, stops := range [][]store.OperatorScopeEntry{forward, reversed} {
		got := LateFactsByFact([]model.Fact{fact}, stops)
		if got["f_1"].StopID != "os-late" {
			t.Fatalf("stop = %q with order %v, want os-late", got["f_1"].StopID, stopIDs(stops))
		}
	}
}

func stopIDs(entries []store.OperatorScopeEntry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.ID)
	}
	return out
}

// No scope entries (or only authorizations) is an empty annotation, not a
// fallback heuristic.
func TestLateFactsByFactEmptyScope(t *testing.T) {
	fact := model.Fact{ID: "f_1", EntityRef: "host:a", CreatedAt: time.Now()}
	if got := LateFactsByFact([]model.Fact{fact}, nil); len(got) != 0 {
		t.Fatalf("nil scope annotated facts: %+v", got)
	}
	auth := []store.OperatorScopeEntry{{ID: "os-a", Kind: store.OperatorScopeKindAuthorization, At: fact.CreatedAt.Add(-time.Hour), EntityRefs: []string{"host:a"}}}
	if got := LateFactsByFact([]model.Fact{fact}, auth); len(got) != 0 {
		t.Fatalf("authorization annotated facts: %+v", got)
	}
}
