package model

import "fmt"

// FactIndex is the one place a fact-id index is built for playbook provenance.
// Callers must not keep their own map: two indexes over different snapshots
// would let the "[确认]" label and the recall gate answer differently for the
// same row.
func FactIndex(facts []Fact) map[string]Fact {
	index := make(map[string]Fact, len(facts))
	for _, f := range facts {
		index[f.ID] = f
	}
	return index
}

// SourceProblem is the single provenance predicate: it returns the first reason
// a playbook's sources cannot support the "confirmed" label, or "" when every
// source qualifies. The check order is fixed so one row always reports the same
// reason: source missing / not verified / no evidence / withdrawn or rejected.
//
// A fact may prove a tactic only when a verifier proved the current assertion
// (Status==verified) and the proof carries bytes (EvidenceIDs). A note is never
// reviewed, so a playbook built on notes stays a candidate forever — that is
// deliberate: the label means "this was re-run and held", not "the writer said
// so".
func (p Playbook) SourceProblem(facts map[string]Fact) string {
	for _, id := range p.FromFactIDs {
		f, ok := facts[id]
		if !ok {
			return fmt.Sprintf("来源事实 %s 不存在", id)
		}
		stamped, bound := p.FromFactClaims[id]
		if !bound {
			return fmt.Sprintf("来源事实 %s 的确认版本未知，需按当前断言重新确认", id)
		}
		if f.ClaimVersion != stamped {
			return fmt.Sprintf("来源事实 %s 已更新（确认时 v%d，当前 v%d），需按新版本重新确认", id, stamped, f.ClaimVersion)
		}
		if f.Status != "verified" {
			return fmt.Sprintf("来源事实 %s 未核验（status=%s）", id, f.Status)
		}
		if len(f.EvidenceIDs) == 0 {
			return fmt.Sprintf("来源事实 %s 没有原始证据", id)
		}
		if f.Unusable() {
			// Unreachable today: a withdrawn or rejected row already fails the
			// verified check above. It stays last and explicit so a future
			// status that is both "verified" and retracted cannot pass silently.
			return fmt.Sprintf("来源事实 %s 已撤回或已否定", id)
		}
	}
	return ""
}

// WithSourceState annotates a copy with the read-side source judgement. The
// stored Status is the writer's declaration and is never rewritten; SourceChanged
// says that a stored confirmation no longer rests on the current claims.
func (p Playbook) WithSourceState(facts map[string]Fact) Playbook {
	p.SourceChange = p.SourceProblem(facts)
	p.SourceChanged = p.SourceChange != "" && p.Status == PlaybookConfirmed
	return p
}

// Confirmed is the only reading of the confirmation label. Status records what
// the writer declared; it is never the answer by itself. A confirmed label needs
// a non-empty provenance and every source to pass SourceProblem, re-derived on
// every read: a source withdrawn afterwards downgrades the row without rewriting
// it, and a later passing review restores it without a migration.
func (p Playbook) Confirmed(facts map[string]Fact) bool {
	return p.Status == PlaybookConfirmed && len(p.FromFactIDs) > 0 && p.SourceProblem(facts) == ""
}
