package api

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/kksty/saker/internal/model"
)

// The inbox lists what waits for a person, but a closed request still has to be
// readable: "dismissed" next to a resolution that says the work resumed is the
// overlap the operator has to judge, and it is invisible if the read only
// returns pending rows. A closed row is history, so it must not count as work.
func TestInboxKeepsClosedRowsWithoutCountingThem(t *testing.T) {
	s, _, st := newCommanderServer(t)
	req, created, err := st.EnsureIdleOperatorRequest()
	if err != nil || !created {
		t.Fatalf("ensure: created=%v err=%v", created, err)
	}
	// Work resumed closes the generic fallback and records why.
	if err := st.DismissIdleOperatorRequests("work_resumed"); err != nil {
		t.Fatal(err)
	}
	rec := call(t, s, "GET", "/api/operator-inbox", nil)
	if rec.Code != 200 {
		t.Fatalf("inbox = %d %s", rec.Code, rec.Body.String())
	}
	var inbox struct {
		Authorization []struct {
			ID string `json:"id"`
		} `json:"authorization"`
		Recent []struct {
			ID         string     `json:"id"`
			Status     string     `json:"status"`
			Resolution string     `json:"resolution"`
			ResolvedAt *time.Time `json:"resolved_at"`
		} `json:"recent"`
		Total   int `json:"total"`
		Summary struct {
			Total int `json:"total"`
		} `json:"summary"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &inbox); err != nil {
		t.Fatal(err)
	}
	if len(inbox.Authorization) != 0 {
		t.Fatalf("closed request still listed as pending: %+v", inbox.Authorization)
	}
	if len(inbox.Recent) != 1 || inbox.Recent[0].ID != req.ID {
		t.Fatalf("closed request missing from recent: %+v", inbox.Recent)
	}
	row := inbox.Recent[0]
	if row.Status != model.OperatorRequestDismissed || row.Resolution != "work_resumed" || row.ResolvedAt == nil {
		t.Fatalf("closed row lost its receipt: %+v", row)
	}
	if inbox.Total != 0 || inbox.Summary.Total != 0 {
		t.Fatalf("a closed row was counted as work: total=%d summary=%d", inbox.Total, inbox.Summary.Total)
	}
}

// Recent is bounded and ordered: history grows without limit, a payload read on
// a page refresh cannot.
func TestRecentInboxRowsAreBoundedAndNewestFirst(t *testing.T) {
	now := time.Now()
	all := []model.OperatorRequest{
		{ID: "pending", Status: model.OperatorRequestPending},
		{ID: "open_no_time", Status: model.OperatorRequestDismissed, CreatedAt: now.Add(-time.Hour)},
	}
	for i := 0; i < 30; i++ {
		resolved := now.Add(time.Duration(i) * time.Minute)
		all = append(all, model.OperatorRequest{
			ID:         fmt.Sprintf("closed_%02d", i),
			Status:     model.OperatorRequestResolved,
			Resolution: "accepted",
			CreatedAt:  now,
			ResolvedAt: &resolved,
		})
	}
	resolvedEarly := now.Add(-2 * time.Hour)
	resolvedLate := now.Add(-time.Minute)
	rows := recentInboxRows(all, inboxRecentLimit)
	if len(rows) != inboxRecentLimit {
		t.Fatalf("recent rows = %d, want %d", len(rows), inboxRecentLimit)
	}
	if rows[0].ID != "closed_29" {
		t.Fatalf("newest resolution is not first: %+v", rows[0])
	}
	for _, row := range rows {
		if row.ID == "pending" {
			t.Fatal("a pending row leaked into recent")
		}
	}
	// A row closed without a resolution time is older than any timed one, so it
	// only survives while the limit leaves room; ordering is checked on a set
	// small enough that it is kept.
	ordering := recentInboxRows([]model.OperatorRequest{
		{ID: "timed_early", Status: model.OperatorRequestResolved, CreatedAt: now, ResolvedAt: &resolvedEarly},
		{ID: "untimed", Status: model.OperatorRequestDismissed, CreatedAt: now.Add(time.Hour)},
		{ID: "timed_late", Status: model.OperatorRequestResolved, CreatedAt: now, ResolvedAt: &resolvedLate},
	}, inboxRecentLimit)
	if len(ordering) != 3 || ordering[0].ID != "timed_late" || ordering[2].ID != "untimed" {
		t.Fatalf("order = %+v, want timed_late, timed_early, untimed", ordering)
	}
}
