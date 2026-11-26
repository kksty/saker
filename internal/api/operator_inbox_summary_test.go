package api

import (
	"testing"

	"github.com/kksty/saker/internal/model"
)

func summaryCategory(t *testing.T, summary operatorInboxSummary, kind string) operatorInboxCategory {
	t.Helper()
	for _, category := range summary.Categories {
		if category.Kind == kind {
			return category
		}
	}
	t.Fatalf("category %q missing from %+v", kind, summary.Categories)
	return operatorInboxCategory{}
}

// The badge and the detail list must describe the same data: the count of a
// category is the number of items the detail view lists, always.
func TestInboxSummaryCountsEqualListedItems(t *testing.T) {
	auth := []model.OperatorRequest{{ID: "or_1", Action: "扩展范围"}, {ID: "or_2", Action: "确认资产"}}
	handoff := []operatorInboxHandoff{{IntentID: "in_1", Title: "等待凭据"}, {IntentID: "in_2", Title: "等待授权"}}
	acceptance := []operatorInboxAcceptance{{GoalID: "g_1", Statement: "拿下后台"}}
	warnings := []boardRunWarnings{{Run: "r_1", IntentID: "in_1", Warnings: []string{"超时"}}}
	guardrail := operatorInboxGuardrail{State: "available", Count: 3, Sessions: map[string]int{"s1": 2, "s2": 1}}

	summary := inboxSummary(auth, handoff, acceptance, warnings, guardrail)
	if !summary.Countable {
		t.Fatal("a fully readable inbox must be countable")
	}
	if summary.Total != 9 {
		t.Fatalf("total = %d, want 9", summary.Total)
	}
	sum := 0
	for _, category := range summary.Categories {
		if category.Count != len(category.Items) && category.Kind != "guardrail" {
			t.Fatalf("%s count %d != listed %d", category.Kind, category.Count, len(category.Items))
		}
		sum += category.Count
	}
	if sum != summary.Total {
		t.Fatalf("categories sum to %d, total says %d", sum, summary.Total)
	}
	// Every class carries the id the detail view opens.
	for _, kind := range []string{"authorization", "handoff", "acceptance", "warning"} {
		category := summaryCategory(t, summary, kind)
		if category.State != inboxStateOK {
			t.Fatalf("%s state = %q", kind, category.State)
		}
		for _, item := range category.Items {
			if item.ID == "" {
				t.Fatalf("%s has an item without an id", kind)
			}
		}
	}
	// The guardrail lists sessions, not calls: one id per session holding a
	// pending approval, while the count stays the host's.
	guardrailCategory := summaryCategory(t, summary, "guardrail")
	if guardrailCategory.Count != 3 || len(guardrailCategory.Items) != 2 {
		t.Fatalf("guardrail = %d over %d sessions", guardrailCategory.Count, len(guardrailCategory.Items))
	}
}

// An unknown answer must never read as "nothing to do": the console renders the
// state, not 0.
func TestInboxSummaryMarksUnreadableCategories(t *testing.T) {
	cases := []struct {
		name  string
		view  operatorInboxGuardrail
		state string
	}{
		{"never asked", operatorInboxGuardrail{State: "never_checked", Reason: "还没有发起工具审批查询"}, inboxStateNeverChecked},
		{"empty state defaults to never asked", operatorInboxGuardrail{}, inboxStateNeverChecked},
		{"host unavailable", operatorInboxGuardrail{State: "unavailable", Reason: "宿主不可达"}, inboxStateUnavailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			summary := inboxSummary(nil, nil, nil, nil, tc.view)
			if summary.Countable {
				t.Fatal("an unreadable category must make the inbox not countable")
			}
			category := summaryCategory(t, summary, "guardrail")
			if category.State != tc.state {
				t.Fatalf("state = %q, want %q", category.State, tc.state)
			}
			if category.Reason == "" && tc.view.Reason != "" {
				t.Fatalf("reason %q was dropped", tc.view.Reason)
			}
			// The other classes are local reads and still say what they hold.
			for _, kind := range []string{"authorization", "handoff", "acceptance", "warning"} {
				if got := summaryCategory(t, summary, kind); got.State != inboxStateNone {
					t.Fatalf("%s state = %q", kind, got.State)
				}
			}
		})
	}
}

func TestInboxSummaryEmptyIsZeroAndCountable(t *testing.T) {
	summary := inboxSummary(nil, nil, nil, nil, operatorInboxGuardrail{State: "available"})
	if !summary.Countable || summary.Total != 0 {
		t.Fatalf("empty inbox = countable %v, total %d", summary.Countable, summary.Total)
	}
	for _, category := range summary.Categories {
		if category.State != inboxStateNone {
			t.Fatalf("%s state = %q, want none", category.Kind, category.State)
		}
		if len(category.Items) != 0 {
			t.Fatalf("%s listed items in an empty inbox", category.Kind)
		}
	}
}
