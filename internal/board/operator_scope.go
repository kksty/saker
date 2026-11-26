// Package board's operator-scope projection.
//
// The operator's stop decisions live in <projectDir>/operator-scope.jsonl, not
// in the ledger (no schema change this cycle). This file turns the explicit
// stop rows plus the ledger's facts into the one read-only annotation the board
// and graph need: "this fact was recorded after the operator stopped this
// entity". The rule is identity + time only — the exact entity_ref string the
// operator listed and the fact's recorded creation time — never the fact's
// title, summary, kind or any keyword. Title-based matching is exactly what the
// taskbook forbids, and it would also be wrong: a fact may mention an entity in
// prose without being about it, and the operator's list is the authorisation.
package board

import (
	"time"

	"github.com/kksty/saker/internal/model"
	"github.com/kksty/saker/internal/store"
)

// LateAfterStop is the annotation attached to one fact that arrived after the
// operator stopped its entity. StopAt is the recorded decision time (not the
// fact's observed_at), so a console can show both timestamps and the reader can
// never mistake the annotation for a judgement about the fact itself.
type LateAfterStop struct {
	StopID string    `json:"stop_id"`
	At     time.Time `json:"at"`
}

// OperatorScopeStops is the stop projection of the operator scope, in file
// order. It exists so board/graph code never has to remember the kind filter.
func OperatorScopeStops(entries []store.OperatorScopeEntry) []store.OperatorScopeEntry {
	out := make([]store.OperatorScopeEntry, 0, len(entries))
	for _, e := range entries {
		if e.Kind == store.OperatorScopeKindStop {
			out = append(out, e)
		}
	}
	return out
}

// LateFactsByFact maps fact id -> the most recent applicable stop. A fact is
// annotated when a stop lists the fact's exact entity_ref and the fact was
// recorded strictly after that stop's recorded time. When several stops match,
// the latest stop before the fact wins, which is the stable "the most recent
// stop that was already in force" answer; ties (same at, impossible through the
// append API but possible in a hand-edited file) fall back to file order.
//
// Facts without an entity_ref never match: the program has no identity to
// compare and must not infer one. Facts recorded at or before the stop (the
// "before" baseline) never match.
func LateFactsByFact(facts []model.Fact, entries []store.OperatorScopeEntry) map[string]LateAfterStop {
	stops := OperatorScopeStops(entries)
	out := map[string]LateAfterStop{}
	for _, f := range facts {
		best := -1
		for i := range stops {
			if !stops[i].MatchesLateFact(f) {
				continue
			}
			if best < 0 || stops[i].At.After(stops[best].At) {
				best = i
			}
		}
		if best >= 0 {
			out[f.ID] = LateAfterStop{StopID: stops[best].ID, At: stops[best].At}
		}
	}
	return out
}
