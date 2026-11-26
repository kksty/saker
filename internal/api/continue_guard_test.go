package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/kksty/saker/internal/collab"
	"github.com/kksty/saker/internal/model"
)

// A closed engagement has no goal to judge and no direction to dispatch, so
// "continue" must not open a forced commander round that only re-reads a board
// saying "done". The refusal keeps the pause and names the two actions that do
// have something to act on.
func TestResumeRefusesAClosedProjectWithNothingToContinue(t *testing.T) {
	s, _ := newBindingServer(t)
	seedStampedGoal(t, s.Store)
	s.AutoPause("目标已完成：测试自动暂停")
	if !s.paused.Load() {
		t.Fatal("fixture is not paused")
	}

	rec := call(t, s, http.MethodPost, "/api/project/resume", nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("resume on a closed project = %d: %s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["code"] != "nothing_to_continue" || body["resumed"] != false {
		t.Fatalf("response = %v", body)
	}
	if !s.paused.Load() {
		t.Fatal("a refused resume must leave the project paused")
	}
}

// A mechanical fact notice is not a direction: it must not make "continue"
// spend a round.
func TestResumeIgnoresAFactNoticeAsNothingToContinue(t *testing.T) {
	s, _ := newBindingServer(t)
	seedStampedGoal(t, s.Store)
	s.AutoPause("目标已完成：测试自动暂停")
	if _, _, err := s.Store.AddMessage(model.Message{
		FromParticipantID: model.NoticeSenderID, ToParticipantID: collab.CommanderID,
		Kind: model.NoticeKindFact, NoticeFactID: "f_1", Text: "f_1 v2",
	}); err != nil {
		t.Fatal(err)
	}
	if rec := call(t, s, http.MethodPost, "/api/project/resume", nil); rec.Code != http.StatusConflict {
		t.Fatalf("fact notice alone resumed the project: %d %s", rec.Code, rec.Body.String())
	}
}

// The flexible path stays open: an operator message gives the round something to
// read, so the same click resumes — and the prompt scopes it as a补测 round.
func TestResumeAfterOperatorMessageOnAClosedProject(t *testing.T) {
	s, _ := newBindingServer(t)
	seedStampedGoal(t, s.Store)
	s.AutoPause("目标已完成：测试自动暂停")
	if _, _, err := s.Store.AddMessage(model.Message{
		FromParticipantID: collab.OperatorID, ToParticipantID: collab.CommanderID,
		Text: "再用只读方式看一下 /admin 的响应头",
	}); err != nil {
		t.Fatal(err)
	}
	if !s.hasOperatorInput() || !s.goalsClosed() {
		t.Fatal("fixture should be a closed project with operator input")
	}
	if rec := call(t, s, http.MethodPost, "/api/project/resume", nil); rec.Code != http.StatusOK {
		t.Fatalf("resume with an operator message = %d: %s", rec.Code, rec.Body.String())
	}
	if s.paused.Load() {
		t.Fatal("resume did not clear the pause")
	}
}

// A parked direction is still a durable direction with a reason. The operator's
// explicit resume is the event that asks the commander to revisit it; refusing
// the click would force a chat message just to make "continue" work.
func TestResumeKeepsAParkedDirectionResumable(t *testing.T) {
	s, _ := newBindingServer(t)
	seedStampedGoal(t, s.Store)
	in, err := s.Store.CreateIntent(model.Intent{Title: "等操作员处理"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Store.UpdateIntent(in.ID, func(i *model.Intent) {
		i.Status = model.StatusParked
		i.Hold = true
	}); err != nil {
		t.Fatal(err)
	}
	s.AutoPause("目标已完成：测试自动暂停")
	if rec := call(t, s, http.MethodPost, "/api/project/resume", nil); rec.Code != http.StatusOK {
		t.Fatalf("resume with a parked direction = %d: %s", rec.Code, rec.Body.String())
	}
}

// An open goal is work, and a project without goals is not finished: both must
// keep resuming exactly as before.
func TestResumeKeepsWorkingForAnOpenGoalAndForNoGoals(t *testing.T) {
	s, _ := newBindingServer(t)
	if rec := call(t, s, http.MethodPost, "/api/project/pause", nil); rec.Code != http.StatusOK {
		t.Fatal(rec.Body.String())
	}
	if rec := call(t, s, http.MethodPost, "/api/project/resume", nil); rec.Code != http.StatusOK {
		t.Fatalf("no-goal project was refused: %d %s", rec.Code, rec.Body.String())
	}

	open, _ := newBindingServer(t)
	if _, err := open.Store.AddGoal(model.Goal{Statement: "还没达成的目标"}); err != nil {
		t.Fatal(err)
	}
	open.AutoPause("测试暂停")
	if rec := call(t, open, http.MethodPost, "/api/project/resume", nil); rec.Code != http.StatusOK {
		t.Fatalf("open goal was refused: %d %s", rec.Code, rec.Body.String())
	}
}
