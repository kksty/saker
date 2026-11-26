package store

import (
	"fmt"
	"strings"

	"github.com/kksty/saker/internal/model"
)

// DispatchGate is how one dispatch treats necessary premises.
//
// The premise gate forks on the effective review mode, which is what AGENTS.md
// says and what the operator actually chose:
//   - reviews open: a necessary premise must carry a verdict at its current
//     claim version (verified_only is the admission mode too), so an unverified
//     premise stops the dispatch with a reason;
//   - reviews closed: no verdict can arrive, so unverified facts may be used as
//     premises and the run records that they were unverified (status stays
//     unverified); only withdrawn/rejected (and kinds a review can never
//     establish) still stop it.
//
// Integrity gaps in the evidence rows — a registered original that is gone or
// carries no digest — are recorded on the run and never stop a dispatch: an
// incomplete evidence chain is an observation about the record, not a reason to
// keep an engagement from being tested.
type DispatchGate struct {
	// ReviewsOff is the project's effective review switch.
	ReviewsOff bool
}

// PrepareRunInput freezes the actual input and its versions together.
// Claiming a task happens earlier: observations can change during preparation.
//
// The returned gaps are the premise problems this dispatch carried forward but
// did not refuse: the caller records them on the run. A refused premise returns
// an error instead and leaves the run unfrozen.
//
// Three sets are frozen because they answer different questions later:
//   - PremiseVersions: the required facts this dispatch is based on (their
//     record revisions, used by the change views);
//   - PremiseClaimVersions: the assertion versions a reviewer would need to
//     reproduce a conclusion from this input;
//   - ContextFactVersions: the explicit references/pins the task selected, so a
//     later focus projection can tell what the run actually saw.
func (s *Store) PrepareRunInput(runID string, task model.Intent, policy string, gate DispatchGate) (DB, []string, error) {
	off := gate.ReviewsOff
	if !model.ValidPremisePolicy(policy) {
		return DB{}, nil, fmt.Errorf("unknown premise policy %q", policy)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	index := -1
	for i, run := range s.db.Runs {
		if run.ID == runID && run.IntentID == task.ID && run.Status == model.RunRunning {
			index = i
			break
		}
	}
	if index < 0 {
		return DB{}, nil, fmt.Errorf("run is no longer active")
	}
	current, ok := s.intentLocked(task.ID)
	if !ok || current.RunID != runID || current.Status != model.StatusRunning {
		return DB{}, nil, fmt.Errorf("run no longer owns the intent")
	}
	// Invariant, not the gate: BeginHandoff refuses an unarchived source before
	// it stops the old run or writes a handoff snapshot, so this branch is only
	// reachable when a ledger was written around that gate. It still refuses —
	// degrading to "trust the current settings" is precisely how a summary
	// becomes a boundary — but a refusal here cannot be the operator's route,
	// because by now the direction already belongs to this run.
	if task.Handoff != nil && strings.TrimSpace(task.Handoff.FromRun) != "" {
		var src model.Run
		found := false
		for _, r := range s.db.Runs {
			if r.ID == task.Handoff.FromRun {
				src, found = r, true
				break
			}
		}
		if !found || strings.TrimSpace(src.BriefPath) == "" {
			return DB{}, nil, fmt.Errorf("%w：被替换的 run %s 没有 brief_path，无法在看不到原文的情况下接手；先重新下发该意图生成归档，再交接",
				ErrHandoffUnarchived, task.Handoff.FromRun)
		}
	}
	versions := make(map[string]uint64, len(task.FromFactIDs))
	claims := make(map[string]uint64, len(task.FromFactIDs))
	var gaps []string
	for _, id := range task.FromFactIDs {
		r, ok := s.records[recordKey("fact", id)]
		if !ok {
			return DB{}, nil, fmt.Errorf("premise %s is missing", id)
		}
		f := r.value.(model.Fact)
		// A premise written by a direction that has not settled is not a
		// premise yet: the row can still be corrected under the direction that
		// cites it, and the downstream would be pinned to a version that no
		// longer exists. This is an ordering rule, not a review gate — the same
		// shape as depending only on facts that have already been produced.
		// Waiting for the owner to settle, or citing the row as context instead,
		// are the two ways out.
		if owner := s.PremiseOwnerStillLive(f, task.ID); owner != "" {
			return DB{}, nil, fmt.Errorf("premise %s：来源 %s 还没收口，现在不能当必要前提；等它收口，或改用 context_fact_ids 只读参考", id, owner)
		}
		if issue := model.PremiseIssue(f, policy); issue != "" {
			if !(off && model.PremiseIssueReviewOnly(f)) {
				return DB{}, nil, fmt.Errorf("premise %s claim v%d (%s): %s", id, f.ClaimVersion, policy, issue)
			}
			// Reviews are closed, so no verdict can arrive; the fact is usable
			// and the run records that it was not verified.
			gaps = append(gaps, fmt.Sprintf("前提 %s：断言 v%d 未核验（复核关闭）", id, f.ClaimVersion))
		}
		// A registered original that is gone or cannot back the claim is an
		// integrity gap in the record. It is recorded, not turned into a veto:
		// the direction may still be tested while the evidence chain is thin.
		for _, eid := range f.EvidenceIDs {
			var row model.Evidence
			found := false
			for _, e := range s.db.Evidence {
				if e.ID == eid {
					row, found = e, true
					break
				}
			}
			if !found {
				gaps = append(gaps, fmt.Sprintf("前提 %s 的原件 %s 不在账本里", id, eid))
				continue
			}
			if !EvidenceVerifiable(row) {
				gaps = append(gaps, fmt.Sprintf("前提 %s 的原件 %s 没有保全摘要", id, eid))
			}
		}
		if policy == model.PremiseVerifiedOnly && !off {
			if _, proven := model.CurrentProof(f, s.db.Reviews); !proven {
				return DB{}, nil, fmt.Errorf("premise %s 缺少当前断言的独立核验收据", id)
			}
		}
		versions[id] = r.version
		claims[id] = f.ClaimVersion
	}
	// References and pins are context, not prerequisites: a row that vanished
	// (or an id the caller mistyped) must not block the dispatch. It is simply
	// absent from the frozen set, and the focus projection reports it missing.
	contextVersions := make(map[string]uint64, len(task.ContextFactIDs)+len(task.PinnedFactIDs))
	for _, id := range append(append([]string(nil), task.ContextFactIDs...), task.PinnedFactIDs...) {
		if r, ok := s.records[recordKey("fact", id)]; ok {
			contextVersions[id] = r.version
		}
	}
	s.db.Runs[index].PremiseVersions = versions
	s.db.Runs[index].PremisePolicy = policy
	s.db.Runs[index].ReviewSkipped = off
	s.db.Runs[index].PremiseClaimVersions = claims
	s.db.Runs[index].ContextFactVersions = contextVersions
	if err := s.flush("run"); err != nil {
		return DB{}, nil, err
	}
	db := s.db.clone()
	for i := range db.Intents {
		if db.Intents[i].ID != task.ID {
			continue
		}
		// Freeze the selection on the snapshot too: the prompt builder and the
		// focus writer must read the same three lists the versions were taken
		// from, not a live intent row that changed while preparing.
		db.Intents[i].FromFactIDs = append([]string(nil), task.FromFactIDs...)
		db.Intents[i].ContextFactIDs = append([]string(nil), task.ContextFactIDs...)
		db.Intents[i].PinnedFactIDs = append([]string(nil), task.PinnedFactIDs...)
		db.Intents[i].InterestEntityRefs = append([]string(nil), task.InterestEntityRefs...)
	}
	return db, gaps, nil
}

// premiseOwnerStillLive names the run or direction that still owns this fact and
// has not settled. Settlement is what makes an assertion stable enough to be a
// necessary premise; while the producer is still running (or its direction is
// waiting on children) the row may be corrected, and a downstream that mounted
// it would point at a version that can change underneath. A fact with no source
// run falls back to its direction; the dispatching direction itself is exempt so
// a retry/continue can cite what its own earlier run produced.
func (s *Store) PremiseOwnerStillLive(f model.Fact, selfIntent string) string {
	// The dispatching direction may cite what its own earlier run produced:
	// an intent has one run at a time, so its own facts are its own material.
	if f.IntentID != "" && f.IntentID == selfIntent {
		return ""
	}
	// Both checks run: a multi-round direction can have a settled first run
	// while its second run is live, and citing the first run's fact is exactly
	// the case the rule is about. Checking only the run let that through.
	if f.SourceRunID != "" {
		for _, r := range s.db.Runs {
			if r.ID == f.SourceRunID && r.Status == model.RunRunning {
				return "run " + r.ID
			}
		}
	}
	if f.IntentID == "" {
		return ""
	}
	for _, i := range s.db.Intents {
		if i.ID != f.IntentID {
			continue
		}
		switch i.Status {
		case model.StatusRunning, model.StatusWaiting:
			return "意图 " + i.ID
		}
	}
	return ""
}
