package api

import (
	"github.com/kksty/saker/internal/model"
)

// Operator inbox states. A category is in exactly one of them, and the console
// renders the state instead of a number when the answer is unknown: a badge that
// reads 0 because the host was never asked is a lie the operator acts on.
const (
	inboxStateOK           = "ok"            // read, and there is something to do
	inboxStateNone         = "none"          // read, and there is nothing
	inboxStateNeverChecked = "never_checked" // nobody asked yet
	inboxStateUnavailable  = "unavailable"   // asked, no usable answer
)

// operatorInboxItem is one actionable record with the id the console needs to
// open it. The console owns routing; the backend only guarantees that the id is
// the same one the detail view uses.
type operatorInboxItem struct {
	Kind  string `json:"kind"`
	ID    string `json:"id"`
	Title string `json:"title,omitempty"`
}

// operatorInboxCategory is one class of "needs a human" work with its state.
type operatorInboxCategory struct {
	Kind   string              `json:"kind"`
	State  string              `json:"state"`
	Reason string              `json:"reason,omitempty"`
	Count  int                 `json:"count"`
	Items  []operatorInboxItem `json:"items"`
}

// operatorInboxSummary is the single source of truth for the three surfaces that
// must agree: the top-bar badge, the overview card and the detail list. None of
// them counts categories itself — they read Total and the per-category counts
// here, so the numbers cannot drift apart.
type operatorInboxSummary struct {
	Categories []operatorInboxCategory `json:"categories"`
	Total      int                     `json:"total"`
	// Countable is false when at least one category is unknown. The console must
	// then show that state ("未查询"/"不可用") rather than the number, including
	// rather than 0.
	Countable bool `json:"countable"`
}

// inboxSummary builds the summary from the same slices the detail response
// carries. That is the point: one read produces both, so the badge and the list
// can never describe different data.
func inboxSummary(
	auth []model.OperatorRequest,
	handoff []operatorInboxHandoff,
	acceptance []operatorInboxAcceptance,
	warnings []boardRunWarnings,
	guardrail operatorInboxGuardrail,
) operatorInboxSummary {
	authItems := make([]operatorInboxItem, 0, len(auth))
	for _, req := range auth {
		authItems = append(authItems, operatorInboxItem{Kind: "authorization", ID: req.ID, Title: req.Action})
	}
	handoffItems := make([]operatorInboxItem, 0, len(handoff))
	for _, h := range handoff {
		handoffItems = append(handoffItems, operatorInboxItem{Kind: "handoff", ID: h.IntentID, Title: h.Title})
	}
	acceptanceItems := make([]operatorInboxItem, 0, len(acceptance))
	for _, a := range acceptance {
		acceptanceItems = append(acceptanceItems, operatorInboxItem{Kind: "acceptance", ID: a.GoalID, Title: a.Statement})
	}
	warningItems := make([]operatorInboxItem, 0, len(warnings))
	for _, w := range warnings {
		title := w.Run
		if len(w.Warnings) > 0 {
			title = w.Warnings[0]
		}
		warningItems = append(warningItems, operatorInboxItem{Kind: "warning", ID: w.Run, Title: title})
	}

	categories := []operatorInboxCategory{
		localInboxCategory("authorization", authItems),
		localInboxCategory("handoff", handoffItems),
		localInboxCategory("acceptance", acceptanceItems),
		localInboxCategory("warning", warningItems),
		guardrailInboxCategory(guardrail),
	}

	out := operatorInboxSummary{Categories: categories, Countable: true}
	for _, category := range categories {
		switch category.State {
		case inboxStateNeverChecked, inboxStateUnavailable:
			out.Countable = false
		}
		out.Total += category.Count
	}
	return out
}

// localInboxCategory covers the classes read straight from the ledger: the read
// is local, so it can never be "never checked" or "unavailable".
func localInboxCategory(kind string, items []operatorInboxItem) operatorInboxCategory {
	state := inboxStateNone
	if len(items) > 0 {
		state = inboxStateOK
	}
	if items == nil {
		items = []operatorInboxItem{}
	}
	return operatorInboxCategory{Kind: kind, State: state, Count: len(items), Items: items}
}

// guardrailInboxCategory maps the tool-approval projection, whose answer comes
// from the host and can therefore be unknown. The count is the cached last read;
// it is only reported as a number when that read actually happened.
func guardrailInboxCategory(view operatorInboxGuardrail) operatorInboxCategory {
	category := operatorInboxCategory{Kind: "guardrail", Items: []operatorInboxItem{}}
	switch view.State {
	case inboxStateNeverChecked, "":
		category.State = inboxStateNeverChecked
		category.Reason = view.Reason
	case inboxStateUnavailable:
		category.State = inboxStateUnavailable
		category.Reason = view.Reason
	default: // "available"
		category.Count = view.Count
		if view.Count > 0 {
			category.State = inboxStateOK
			category.Items = guardrailItems(view)
		} else {
			category.State = inboxStateNone
		}
	}
	return category
}

// guardrailItems lists the sessions that hold a pending approval, one id per
// session: the host reports counts per session, not per call.
func guardrailItems(view operatorInboxGuardrail) []operatorInboxItem {
	items := make([]operatorInboxItem, 0, len(view.Sessions))
	for sessionID, count := range view.Sessions {
		if count <= 0 {
			continue
		}
		items = append(items, operatorInboxItem{Kind: "guardrail", ID: sessionID})
	}
	return items
}
