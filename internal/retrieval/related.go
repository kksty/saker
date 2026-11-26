package retrieval

import (
	"sort"
	"strings"

	"github.com/kksty/saker/internal/model"
	"github.com/kksty/saker/internal/store"
)

// RelatedIntents answers one question for the commander: which existing
// directions may be the same work as this proposal? It is read-only. Sharing a
// recorded premise or entity is evidence of overlap, not a decision: the
// commander still decides whether to merge, continue the old direction, reject
// the proposal or approve it as new work. Nothing here changes dispatch
// admission.
type RelatedIntents struct {
	ProposalID string          `json:"proposal_id"`
	Candidates []RelatedIntent `json:"candidates"`
	// Note states the boundary on the surface that carries the candidates.
	Note string `json:"note"`
}

// RelatedIntent is one candidate direction plus why it matched.
type RelatedIntent struct {
	IntentID string `json:"intent_id"`
	Title    string `json:"title"`
	// Eval is the direction's acceptance criterion, kept next to the candidate
	// so two directions on one entity are comparable instead of assumed equal.
	Eval      string `json:"eval,omitempty"`
	Status    string `json:"status"`
	RunID     string `json:"run_id,omitempty"`
	RunStatus string `json:"run_status,omitempty"`
	// Held means an operator/commander stop owns the row; it is a decision input,
	// not an admission gate here.
	Held bool `json:"held,omitempty"`
	// Parked/Reopenable tell the commander whether the old direction can resume
	// instead of creating a duplicate.
	Parked     bool            `json:"parked,omitempty"`
	Reopenable bool            `json:"reopenable,omitempty"`
	Reasons    []RelatedReason `json:"reasons"`
}

// RelatedReason is one recorded link between the proposal and a candidate.
// Value is a fact id for shared_premise and an entity identity for shared_entity.
type RelatedReason struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
	Title string `json:"title,omitempty"`
}

const (
	relatedSharedPremise = "shared_premise"
	relatedSharedEntity  = "shared_entity"
)

// relatedNote is deliberately not an admission statement: the endpoint exists
// so a commander can look, not so the platform can gate.
const relatedNote = "候选只按账本里已记录的共同必要前提或实体引用列出；是否重复、合并、续跑或另立由指挥官判断。候选不改变批准、派发或任何准入检查。"

// RelatedIntentsFor builds the candidate list from the ledger snapshot. It
// reads the proposal's necessary premises and the entity references carried by
// the facts it cites (plus its source direction's explicit entity interests),
// then matches them against every other direction's recorded premises/entities.
// Title similarity is not used: two different titles can be the same work.
func RelatedIntentsFor(db store.DB, p model.Proposal, limit int) RelatedIntents {
	out := RelatedIntents{ProposalID: p.ID, Candidates: []RelatedIntent{}, Note: relatedNote}
	if limit <= 0 {
		limit = 20
	}
	factByID := make(map[string]model.Fact, len(db.Facts))
	for _, f := range db.Facts {
		factByID[f.ID] = f
	}
	runByID := make(map[string]model.Run, len(db.Runs))
	for _, r := range db.Runs {
		runByID[r.ID] = r
	}
	premises := make(map[string]bool, len(p.FromFactIDs))
	for _, id := range p.FromFactIDs {
		premises[id] = true
	}
	entities := map[string]bool{}
	collectEntities := func(ids []string) {
		for _, id := range ids {
			if f, ok := factByID[id]; ok && strings.TrimSpace(f.EntityRef) != "" {
				entities[strings.TrimSpace(f.EntityRef)] = true
			}
		}
	}
	collectEntities(p.FromFactIDs)
	collectEntities(p.ContextFactIDs)
	collectEntities(p.PinnedFactIDs)
	// The proposal's source direction may carry explicit entity interests that
	// its premise facts do not repeat.
	if p.FromIntentID != "" {
		for _, in := range db.Intents {
			if in.ID != p.FromIntentID {
				continue
			}
			for _, ref := range in.InterestEntityRefs {
				if ref = strings.TrimSpace(ref); ref != "" {
					entities[ref] = true
				}
			}
		}
	}

	for _, in := range db.Intents {
		if in.ID == p.FromIntentID {
			continue
		}
		var reasons []RelatedReason
		for _, id := range in.FromFactIDs {
			if !premises[id] {
				continue
			}
			reason := RelatedReason{Kind: relatedSharedPremise, Value: id}
			if f, ok := factByID[id]; ok {
				reason.Title = f.Title
			}
			reasons = append(reasons, reason)
		}
		candidateEntities := map[string]bool{}
		for _, ref := range in.InterestEntityRefs {
			if ref = strings.TrimSpace(ref); ref != "" {
				candidateEntities[ref] = true
			}
		}
		for _, id := range append(append([]string(nil), in.FromFactIDs...), in.ContextFactIDs...) {
			if f, ok := factByID[id]; ok && strings.TrimSpace(f.EntityRef) != "" {
				candidateEntities[strings.TrimSpace(f.EntityRef)] = true
			}
		}
		for ref := range candidateEntities {
			if entities[ref] {
				reasons = append(reasons, RelatedReason{Kind: relatedSharedEntity, Value: ref})
			}
		}
		if len(reasons) == 0 {
			continue
		}
		sort.SliceStable(reasons, func(i, j int) bool {
			if reasons[i].Kind != reasons[j].Kind {
				return reasons[i].Kind < reasons[j].Kind
			}
			return reasons[i].Value < reasons[j].Value
		})
		cand := RelatedIntent{
			IntentID: in.ID, Title: in.Title, Eval: in.Eval, Status: in.Status,
			RunID: in.RunID, Held: in.Hold, Reasons: reasons,
			Parked: in.Status == model.StatusParked, Reopenable: in.Status == model.StatusParked,
		}
		if r, ok := runByID[in.RunID]; ok {
			cand.RunStatus = r.Status
		}
		out.Candidates = append(out.Candidates, cand)
	}
	sort.SliceStable(out.Candidates, func(i, j int) bool {
		a, b := out.Candidates[i], out.Candidates[j]
		if n, m := premiseCount(a.Reasons), premiseCount(b.Reasons); n != m {
			return n > m
		}
		return a.IntentID < b.IntentID
	})
	if len(out.Candidates) > limit {
		out.Candidates = out.Candidates[:limit]
	}
	return out
}

func premiseCount(reasons []RelatedReason) int {
	n := 0
	for _, r := range reasons {
		if r.Kind == relatedSharedPremise {
			n++
		}
	}
	return n
}
