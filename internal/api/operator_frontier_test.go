package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kksty/saker/internal/board"
	"github.com/kksty/saker/internal/model"
)

func TestOperatorBoardExplainsBlockersWithoutChangingSessionFrontier(t *testing.T) {
	s, project, awaitingReview, _ := proofPolicyFixture(t)
	noEvidence, err := project.Store.AddFact(model.Fact{Kind: "artifact", Title: "sample metadata"})
	if err != nil {
		t.Fatal(err)
	}
	create := func(in model.Intent) string {
		t.Helper()
		created, err := project.Store.CreateIntent(in)
		if err != nil {
			t.Fatal(err)
		}
		return created.ID
	}
	parked := create(model.Intent{Title: "parked direction", FromFactIDs: []string{noEvidence.ID}})
	if _, err := project.Store.UpdateIntent(parked, func(in *model.Intent) {
		in.Status = model.StatusParked
		in.Hold = true
	}); err != nil {
		t.Fatal(err)
	}
	review := create(model.Intent{Title: "pending direction", FromFactIDs: []string{awaitingReview.ID}})
	held := create(model.Intent{Title: "held direction", Hold: true})

	readOperator := func() map[string]operatorFrontierItem {
		t.Helper()
		rec := serveMux(t, s, httptest.NewRequest(http.MethodGet, "/api/board", nil))
		var body struct {
			Items []operatorFrontierItem `json:"operator_frontier"`
		}
		if rec.Code != http.StatusOK {
			t.Fatalf("operator board: %d %s", rec.Code, rec.Body.String())
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		rows := make(map[string]operatorFrontierItem, len(body.Items))
		for _, row := range body.Items {
			rows[row.ID] = row
		}
		return rows
	}
	// Missing evidence is an advisory annotation, never a blocker: with review
	// on, the parked row is blocked by the verdict gate and still names the
	// unevidenced premise; with review off it is ready and the annotation stays.
	assertNoEvidence := func(row operatorFrontierItem) {
		t.Helper()
		if len(row.NoEvidenceFacts) != 1 || row.NoEvidenceFacts[0] != noEvidence.ID {
			t.Fatalf("no-evidence annotation: %+v", row)
		}
	}
	rows := readOperator()
	assertNoEvidence(rows[parked])
	if row := rows[parked]; row.Ready || len(row.PremiseIssues) != 1 ||
		!strings.Contains(row.PremiseIssues[0].Reason, "尚未通过独立核验") {
		t.Fatalf("parked row (review on): %+v", row)
	}
	if row := rows[review]; row.Ready || len(row.PremiseIssues) != 1 ||
		row.PremiseIssues[0].FactID != awaitingReview.ID ||
		!strings.Contains(row.PremiseIssues[0].Reason, "尚未通过独立核验") {
		t.Fatalf("awaiting review row: %+v", row)
	}
	if row := rows[held]; row.Ready || !row.Held || len(row.PremiseIssues) != 0 {
		t.Fatalf("held row: %+v", row)
	}
	s.SetReviewsDisabled(true)
	off := readOperator()
	assertNoEvidence(off[parked])
	if row := off[parked]; len(row.PremiseIssues) != 0 {
		t.Fatalf("parked row (review off) still blocked by a premise: %+v", row)
	}

	for _, req := range []*http.Request{
		agentRequest(s, http.MethodGet, "/api/board"),
		harnessRequest(http.MethodGet, "/api/board"),
	} {
		rec := serveMux(t, s, req)
		var body struct {
			Frontier         []board.FrontierItem `json:"frontier"`
			OperatorFrontier json.RawMessage      `json:"operator_frontier"`
		}
		if rec.Code != http.StatusOK {
			t.Fatalf("session board: %d %s", rec.Code, rec.Body.String())
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if len(body.OperatorFrontier) != 0 {
			t.Fatal("operator blocker details leaked to a session")
		}
		for _, row := range body.Frontier {
			if row.ID == parked {
				t.Fatal("parked direction was added to the session frontier")
			}
		}
	}
}
