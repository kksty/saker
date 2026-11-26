package store

import "github.com/kksty/saker/internal/model"

func detachGoal(g model.Goal) model.Goal {
	if g.ClosedAt != nil {
		at := *g.ClosedAt
		g.ClosedAt = &at
	}
	g.From = append([]string(nil), g.From...)
	g.FromClaimVersions = copyVersions(g.FromClaimVersions)
	g.ReceiptChanged = append([]string(nil), g.ReceiptChanged...)
	if g.CompletedAt != nil {
		at := *g.CompletedAt
		g.CompletedAt = &at
	}
	return g
}

// markGoalReceipt computes whether a completed goal's receipt still rests on the
// claims it cited. It is a read-side observation: the stored receipt is left
// exactly as it was stamped, so "what did we prove at the time" stays answerable.
func markGoalReceipt(g model.Goal, facts []model.Fact) model.Goal {
	g.ReceiptStale, g.ReceiptChanged = false, nil
	if !g.Met || len(g.FromClaimVersions) == 0 {
		return g
	}
	byID := make(map[string]uint64, len(facts))
	unusable := make(map[string]bool)
	for i := range facts {
		byID[facts[i].ID] = facts[i].ClaimVersion
		unusable[facts[i].ID] = facts[i].Unusable()
	}
	for _, id := range g.From {
		stamped, ok := g.FromClaimVersions[id]
		if !ok {
			continue
		}
		current, exists := byID[id]
		if !exists || current != stamped || unusable[id] {
			g.ReceiptChanged = append(g.ReceiptChanged, id)
		}
	}
	if len(g.ReceiptChanged) > 0 {
		g.ReceiptStale = true
	}
	return g
}

// markIntentDoneReceipt computes whether a declared completion still rests on
// the claims the settler accepted. It mirrors markGoalReceipt: the stored
// receipt (note, ids, claim versions and statuses) is never rewritten, so the
// original judgement stays auditable after the underlying fact moves.
func markIntentDoneReceipt(in model.Intent, facts []model.Fact) model.Intent {
	in.DoneReceiptStale, in.DoneReceiptChanged = false, nil
	if !in.DoneDeclared() || len(in.DoneFactClaims) == 0 {
		return in
	}
	byID := make(map[string]model.Fact, len(facts))
	for i := range facts {
		byID[facts[i].ID] = facts[i]
	}
	for _, id := range in.DoneFactIDs {
		stamped, ok := in.DoneFactClaims[id]
		if !ok {
			continue
		}
		f, exists := byID[id]
		if !exists || f.ClaimVersion != stamped || f.Unusable() {
			in.DoneReceiptChanged = append(in.DoneReceiptChanged, id)
			continue
		}
		// The assertion did not change, but the verdict that backed the receipt
		// did (e.g. a re-review asked for evidence). A display edit never reaches
		// this branch: title and other labels are outside the claim hash.
		if was := in.DoneFactStatuses[id]; was == "verified" && f.Status != "verified" {
			in.DoneReceiptChanged = append(in.DoneReceiptChanged, id)
		}
	}
	in.DoneReceiptStale = len(in.DoneReceiptChanged) > 0
	return in
}

func detachPlaybook(p model.Playbook) model.Playbook {
	p.Tags = append([]string(nil), p.Tags...)
	if p.RequiredTags != nil {
		p.RequiredTags = append([]string{}, p.RequiredTags...)
	}
	p.FromFactIDs = append([]string(nil), p.FromFactIDs...)
	if p.FromFactVersions != nil {
		v := map[string]uint64{}
		for k, n := range p.FromFactVersions {
			v[k] = n
		}
		p.FromFactVersions = v
	}
	if p.Pattern != nil {
		pattern := *p.Pattern
		pattern.Nodes = append([]model.PatternNode(nil), pattern.Nodes...)
		pattern.Edges = append([]model.PatternEdge(nil), pattern.Edges...)
		for i := range pattern.Nodes {
			pattern.Nodes[i].Tags = append([]string(nil), pattern.Nodes[i].Tags...)
		}
		p.Pattern = &pattern
	}
	return p
}

func detachFact(f model.Fact) model.Fact {
	if f.ObservedAt != nil {
		at := *f.ObservedAt
		f.ObservedAt = &at
	}
	if f.RetractedAt != nil {
		at := *f.RetractedAt
		f.RetractedAt = &at
	}
	f.EvidenceIDs = append([]string(nil), f.EvidenceIDs...)
	f.PendingEvidence = append([]string(nil), f.PendingEvidence...)
	f.Tags = append([]string(nil), f.Tags...)
	f.Refs = append([]model.Ref(nil), f.Refs...)
	return f
}
func detachRun(r model.Run) model.Run {
	r.NoteWrites = append([]model.NoteRef(nil), r.NoteWrites...)
	if r.NoteRead != nil {
		v := *r.NoteRead
		r.NoteRead = &v
	}
	if r.QueuedAt != nil {
		v := *r.QueuedAt
		r.QueuedAt = &v
	}
	if r.ExecutionStartedAt != nil {
		v := *r.ExecutionStartedAt
		r.ExecutionStartedAt = &v
	}
	if r.QueueMS != nil {
		v := *r.QueueMS
		r.QueueMS = &v
	}
	if r.ExecutionMS != nil {
		v := *r.ExecutionMS
		r.ExecutionMS = &v
	}
	r.ContextFactVersions = copyVersions(r.ContextFactVersions)
	r.PremiseClaimVersions = copyVersions(r.PremiseClaimVersions)
	r.ResourceRefs = append([]string(nil), r.ResourceRefs...)
	r.InstructionVersions = copyVersions(r.InstructionVersions)
	r.InstructionNotices = copyVersions(r.InstructionNotices)
	r.InstructionAcks = copyVersions(r.InstructionAcks)
	if r.PremiseVersions != nil {
		versions := map[string]uint64{}
		for id, v := range r.PremiseVersions {
			versions[id] = v
		}
		r.PremiseVersions = versions
	}
	r.Warnings = append([]string(nil), r.Warnings...)
	r.Infos = append([]string(nil), r.Infos...)
	r.Ingest = detachIngestOutcome(r.Ingest)
	r.WaitDelivery = detachWaitDelivery(r.WaitDelivery)
	if r.DeclarationStart != nil {
		v := *r.DeclarationStart
		r.DeclarationStart = &v
	}
	if r.EndedAt != nil {
		v := *r.EndedAt
		r.EndedAt = &v
	}
	if r.LastEventAt != nil {
		v := *r.LastEventAt
		r.LastEventAt = &v
	}
	if r.LastTurnStartedAt != nil {
		v := *r.LastTurnStartedAt
		r.LastTurnStartedAt = &v
	}
	return r
}

// detachIngestOutcome and detachWaitDelivery give run snapshots their own
// aggregate state: UpdateRun edits the live row in place, and a caller may
// marshal a run while settlement appends a pending declaration.
func detachIngestOutcome(in *model.IngestOutcome) *model.IngestOutcome {
	if in == nil {
		return nil
	}
	out := *in
	out.Pending = append([]model.PendingDeclaration(nil), in.Pending...)
	for i := range out.Pending {
		out.Pending[i].Reasons = append([]string(nil), in.Pending[i].Reasons...)
	}
	out.Reasons = append([]string(nil), in.Reasons...)
	return &out
}

func detachWaitDelivery(d *model.WaitDelivery) *model.WaitDelivery {
	if d == nil {
		return nil
	}
	out := *d
	out.Wait = detachWait(d.Wait)
	if d.ReadyAt != nil {
		v := *d.ReadyAt
		out.ReadyAt = &v
	}
	if d.ArchivedAt != nil {
		v := *d.ArchivedAt
		out.ArchivedAt = &v
	}
	if d.ReceivedAt != nil {
		v := *d.ReceivedAt
		out.ReceivedAt = &v
	}
	return &out
}

func copyVersions(v map[string]uint64) map[string]uint64 {
	if v == nil {
		return nil
	}
	out := make(map[string]uint64, len(v))
	for id, version := range v {
		out[id] = version
	}
	return out
}

// Store inputs and snapshots must own every mutable field. Readers keep these
// values after the lock is released, while later transactions edit live rows.
func detachIntent(in model.Intent) model.Intent {
	in.ContextFactIDs = append([]string(nil), in.ContextFactIDs...)
	in.PinnedFactIDs = append([]string(nil), in.PinnedFactIDs...)
	in.InterestEntityRefs = append([]string(nil), in.InterestEntityRefs...)
	in.FromFactIDs = append([]string(nil), in.FromFactIDs...)
	in.ParkedPremiseFactIDs = append([]string(nil), in.ParkedPremiseFactIDs...)
	if in.ParkedPremiseClaims != nil {
		claims := make(map[string]uint64, len(in.ParkedPremiseClaims))
		for id, version := range in.ParkedPremiseClaims {
			claims[id] = version
		}
		in.ParkedPremiseClaims = claims
	}
	in.DoneFactClaims = copyVersions(in.DoneFactClaims)
	if in.DoneFactStatuses != nil {
		statuses := make(map[string]string, len(in.DoneFactStatuses))
		for id, status := range in.DoneFactStatuses {
			statuses[id] = status
		}
		in.DoneFactStatuses = statuses
	}
	in.DoneReceiptChanged = append([]string(nil), in.DoneReceiptChanged...)
	if in.ParkedAt != nil {
		at := *in.ParkedAt
		in.ParkedAt = &at
	}
	if in.FromFactClaims != nil {
		claims := make(map[string]uint64, len(in.FromFactClaims))
		for id, version := range in.FromFactClaims {
			claims[id] = version
		}
		in.FromFactClaims = claims
	}
	in.Skills = append([]string(nil), in.Skills...)
	in.Interjections = append([]model.Interjection(nil), in.Interjections...)
	in.Notes = append([]model.IntentNote(nil), in.Notes...)
	in.Wait = detachWait(in.Wait)
	if in.Handoff != nil {
		h := *in.Handoff
		h.Wait = detachWait(h.Wait)
		h.Facts = append([]string(nil), h.Facts...)
		h.FactVersions = copyVersions(h.FactVersions)
		h.FactClaimVersions = copyVersions(h.FactClaimVersions)
		h.Evidence = append([]string(nil), h.Evidence...)
		h.Attempts = append([]string(nil), h.Attempts...)
		in.Handoff = &h
	}
	return in
}

// detachProposal gives a snapshot its own slices: the row is updated in place
// under the lock (decisions replace the whole row, callers keep the returned
// value after the lock is released).
func detachProposal(p model.Proposal) model.Proposal {
	p.FromFactIDs = append([]string(nil), p.FromFactIDs...)
	p.ContextFactIDs = append([]string(nil), p.ContextFactIDs...)
	p.PinnedFactIDs = append([]string(nil), p.PinnedFactIDs...)
	p.Skills = append([]string(nil), p.Skills...)
	if p.DecidedAt != nil {
		at := *p.DecidedAt
		p.DecidedAt = &at
	}
	return p
}

func detachWait(wait *model.Wait) *model.Wait {
	if wait == nil {
		return nil
	}
	w := *wait
	w.Actions = append([]string(nil), wait.Actions...)
	if w.Deadline != nil {
		deadline := *w.Deadline
		w.Deadline = &deadline
	}
	w.Results = append([]model.WaitResult(nil), w.Results...)
	for i := range w.Results {
		w.Results[i].FactIDs = append([]string(nil), w.Results[i].FactIDs...)
		w.Results[i].EvidenceIDs = append([]string(nil), w.Results[i].EvidenceIDs...)
	}
	if w.Operator != nil {
		o := *w.Operator
		o.AccessIDs = append([]string(nil), o.AccessIDs...)
		if o.CompletedAt != nil {
			completed := *o.CompletedAt
			o.CompletedAt = &completed
		}
		w.Operator = &o
	}
	return &w
}
