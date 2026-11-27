package api

import (
	"strings"
	"testing"
	"time"

	"github.com/kksty/saker/internal/model"
)

func TestCollaborationSectionKeepsPeersAndHandoff(t *testing.T) {
	s, p := newBindingServer(t)
	self, err := p.Store.CreateIntent(model.Intent{Title: "父意图"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Store.CreateIntent(model.Intent{Title: "另一意图"}); err != nil {
		t.Fatal(err)
	}
	done, err := p.Store.CreateIntent(model.Intent{Title: "已完成意图"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Store.UpdateIntent(done.ID, func(i *model.Intent) { i.Status = model.StatusDone }); err != nil {
		t.Fatal(err)
	}

	section := s.collaborationSection(self)
	for _, want := range []string{"## 自主协作", "申请接班", "另一意图", "pt_in_",
		"cyber://skills/collaborate/SKILL.md", ".saker/identity.json"} {
		if !strings.Contains(section, want) {
			t.Fatalf("collaboration section missing %q:\n%s", want, section)
		}
	}
	// Collaboration is described by what a model may do, never by a role
	// ladder: the prompt must not resurrect 将军/士兵 or a fixed hierarchy.
	for _, forbidden := range []string{"将军", "士兵", "协调者"} {
		if strings.Contains(section, forbidden) {
			t.Fatalf("collaboration section reintroduced a role name %q:\n%s", forbidden, section)
		}
	}
	if strings.Contains(section, "已完成意图") {
		t.Fatalf("finished direction listed as a contactable peer:\n%s", section)
	}
	if strings.Contains(section, "broken") {
		t.Fatalf("unrunnable profile listed as available:\n%s", section)
	}
	for _, deny := range []string{"curl", "POST ", "/spawn", "/children"} {
		if strings.Contains(section, deny) {
			t.Fatalf("retired operation or command found in collaboration section (%q):\n%s", deny, section)
		}
	}
}

func TestCollaborationSectionSoloStillCarriesTheInventory(t *testing.T) {
	s, p := newBindingServer(t)
	self, err := p.Store.CreateIntent(model.Intent{Title: "独跑"})
	if err != nil {
		t.Fatal(err)
	}
	got := s.collaborationSection(self)
	for _, want := range []string{
		"## 自主协作（不必经过指挥官）",
		"- 可用动作：",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("solo section is missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "- 同伴：") {
		t.Fatalf("solo section lists a peer line with no peers:\n%s", got)
	}
}

// The peer line carries what that peer registered, newest first, keeps the
// review label, and drops retracted rows. Before this, deciding whether to ask a
// peer meant peeking at its run events (process) instead of reading the ledger
// (conclusions); a withdrawn title would also have outlived its retraction in
// every other direction's brief.
func TestCollaborationSectionCarriesPeerRecentFacts(t *testing.T) {
	s, p := newBindingServer(t)
	self, err := p.Store.CreateIntent(model.Intent{Title: "自己"})
	if err != nil {
		t.Fatal(err)
	}
	peer, err := p.Store.CreateIntent(model.Intent{Title: "有产出"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Store.CreateIntent(model.Intent{Title: "空手"}); err != nil {
		t.Fatal(err)
	}
	done, err := p.Store.CreateIntent(model.Intent{Title: "已完成"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Store.UpdateIntent(done.ID, func(i *model.Intent) { i.Status = model.StatusDone }); err != nil {
		t.Fatal(err)
	}

	base := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	for i, title := range []string{"旧结论", "较新结论", "最新结论"} {
		f := model.Fact{IntentID: peer.ID, Kind: "host", Title: title, CreatedAt: base.Add(time.Duration(i) * time.Minute)}
		if _, err := p.Store.AddFact(f); err != nil {
			t.Fatal(err)
		}
	}
	retracted, err := p.Store.AddFact(model.Fact{IntentID: peer.ID, Kind: "host", Title: "已撤回结论", CreatedAt: base.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Store.WithdrawFact(retracted.ID, "executor", "", "写入探针，撤回"); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Store.AddFact(model.Fact{IntentID: done.ID, Kind: "host", Title: "收工结论"}); err != nil {
		t.Fatal(err)
	}

	got := s.collaborationSection(self)
	for _, want := range []string{
		"有产出（pt_in_", "：最近事实「最新结论」（未核验）、「较新结论」（未核验）",
		"空手（pt_in_", "：还没有登记事实",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("section missing %q:\n%s", want, got)
		}
	}
	for _, unwanted := range []string{"旧结论", "已撤回结论", "收工结论"} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("section leaked %q:\n%s", unwanted, got)
		}
	}
}
