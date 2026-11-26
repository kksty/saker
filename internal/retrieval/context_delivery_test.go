package retrieval

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/kksty/saker/internal/model"
)

// A delivery is prompt input, not an audit export. The ledger envelope
// (transaction/at/position) and a create row's schema-sized field list say how
// the write was stored, not what changed; a measured 20-create delivery spent
// ~4 KB repeating the same ten column names. An update keeps its field list
// because there it is the news.
func TestDeliveryDropsLedgerEnvelopeAndCreateFieldLists(t *testing.T) {
	s := ledger(t)
	created, err := s.AddFact(model.Fact{Kind: "note", Title: "before"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateFact(created.ID, func(f *model.Fact) { f.Title = "after" }); err != nil {
		t.Fatal(err)
	}
	ctx, err := WorkingContext(s, "commander")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"transaction"`, `"position"`, `"at"`} {
		if strings.Contains(string(raw), key) {
			t.Fatalf("delivery kept the ledger envelope %s: %s", key, raw)
		}
	}
	var create, update *DeliveryChange
	for i := range ctx.Changes.Changes {
		c := &ctx.Changes.Changes[i]
		if c.ID != created.ID {
			continue
		}
		switch c.Operation {
		case "create":
			create = c
		case "update":
			update = c
		}
	}
	if create == nil || update == nil {
		t.Fatalf("fixture changes missing: %+v", ctx.Changes.Changes)
	}
	if len(create.Fields) != 0 {
		t.Fatalf("create kept its schema-sized field list: %v", create.Fields)
	}
	if len(update.Fields) == 0 {
		t.Fatal("update lost the field list that says what changed")
	}
}

func TestSessionDeliveryRechecksWithdrawnFactOnReplay(t *testing.T) {
	s := ledger(t)
	verified, err := s.AddFact(model.Fact{Kind: "note", Title: "verified marker"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReviewFact(model.Review{FactID: verified.ID, Verdict: "pass", Notes: "private receipt marker"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddFact(model.Fact{Kind: "note", Title: "unverified marker"}); err != nil {
		t.Fatal(err)
	}
	first, err := WorkingContext(s, "commander")
	if err != nil {
		t.Fatal(err)
	}
	firstJSON, _ := json.Marshal(first)
	if !strings.Contains(string(firstJSON), "verified marker") || strings.Contains(string(firstJSON), "unverified marker") || strings.Contains(string(firstJSON), "private receipt marker") {
		t.Fatalf("wrong initial visibility: %s", firstJSON)
	}
	if _, err := s.WithdrawFact(verified.ID, "test", "", "fixture"); err != nil {
		t.Fatal(err)
	}
	again, err := WorkingContext(s, "commander")
	if err != nil {
		t.Fatal(err)
	}
	replayJSON, _ := json.Marshal(again)
	if first.DeliveryID != again.DeliveryID || first.Survey.NextCursor != again.Survey.NextCursor || strings.Contains(string(replayJSON), "verified marker") {
		t.Fatalf("replay moved the delivery or exposed withdrawn content: %s", replayJSON)
	}
	page, err := Changes(s, "", 20, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range page.Changes {
		if len(c.Value) != 0 {
			t.Fatalf("the change feed carries a historical value: %+v", c)
		}
	}
}
