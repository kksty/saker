package api

import (
	"testing"

	"github.com/kksty/saker/internal/model"
)

// A human verdict must land on the assertion the operator actually looked at.
// The store only refuses a stale verdict when it is told the version
// (store.go:1114); without it the store binds the verdict to whatever version is
// current at write time (store.go:1145), so a page left open for half a minute
// can reject an assertion nobody read.
func TestOperatorReviewBindsTheVersionThePageShowed(t *testing.T) {
	s, p := newBindingServer(t)
	// reviewed creates a vuln fact at v1 and then re-states its claim, so the
	// ledger is at v2 while a page opened before the edit still shows v1.
	reviewed := func(title string) model.Fact {
		f, err := p.Store.AddFact(model.Fact{Kind: "vuln", Title: title, Summary: "断言 v1", Status: "unverified"})
		if err != nil {
			t.Fatal(err)
		}
		moved, err := p.Store.UpdateFact(f.ID, func(x *model.Fact) { x.Summary = "断言 v2" })
		if err != nil {
			t.Fatal(err)
		}
		if moved.ClaimVersion != 2 {
			t.Fatalf("fixture: claim version = %d, want 2", moved.ClaimVersion)
		}
		return moved
	}

	// The page still shows v1 while the ledger has moved to v2: refuse, never
	// attach the verdict to the version the operator did not read.
	stale := reviewed("页面停在 v1")
	rec := call(t, s, "POST", "/api/facts/"+stale.ID+"/review", map[string]any{
		"verdict": "reject", "notes": "复现不了", "claim_version": 1,
	})
	if rec.Code != 409 {
		t.Fatalf("stale version = %d %s, want 409", rec.Code, rec.Body.String())
	}
	if got, _ := p.Store.FactByID(stale.ID); got.Status != "unverified" {
		t.Fatalf("a refused verdict moved the fact to %q", got.Status)
	}
	if revs := p.Store.Reviews(stale.ID); len(revs) != 0 {
		t.Fatalf("a refused verdict was recorded: %+v", revs)
	}

	// Matching version: accepted.
	current := reviewed("页面与账本一致")
	rec = call(t, s, "POST", "/api/facts/"+current.ID+"/review", map[string]any{
		"verdict": "reject", "notes": "复现不了", "claim_version": 2,
	})
	if rec.Code != 200 {
		t.Fatalf("current version = %d %s", rec.Code, rec.Body.String())
	}

	// No field (an older console): the handler falls back to the version it just
	// read. This is the positive control — the 409 above is about the version,
	// not about refusing reviews.
	old := reviewed("老控制台不带版本")
	rec = call(t, s, "POST", "/api/facts/"+old.ID+"/review", map[string]any{
		"verdict": "reject", "notes": "复现不了",
	})
	if rec.Code != 200 {
		t.Fatalf("no version = %d %s", rec.Code, rec.Body.String())
	}
	revs := p.Store.Reviews(old.ID)
	if len(revs) != 1 || revs[0].ClaimVersion != 2 || revs[0].ClaimHash != old.ClaimHash {
		t.Fatalf("fallback verdict = %+v, want it bound to v2/%s", revs, old.ClaimHash)
	}
}
