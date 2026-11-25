package api

import (
	"strings"
	"testing"

	"github.com/kksty/tianwu/internal/model"
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
		"cyber://skills/collaborate/SKILL.md", ".tw/identity.json"} {
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
