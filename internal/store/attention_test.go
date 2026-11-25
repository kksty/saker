package store

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/kksty/tianwu/internal/collab"
	"github.com/kksty/tianwu/internal/model"
)

// A parked direction whose recorded premise review later passes must raise a
// durable global attention reason instead of leaving the old park prose as the
// only record. This is the race that made two directions look contradictory:
// the review passed 18s before the park was written.
func TestParkedPremiseReadyRaisesAttention(t *testing.T) {
	s := open(t)
	e, err := s.AddEvidence(model.Evidence{
		IntentID: "in_1", Name: "proof.txt", URI: "in_1/raw/proof.txt",
		Digest: strings.Repeat("b", 64), Size: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	f, err := s.AddFact(model.Fact{
		IntentID: "in_1", Kind: "host", Title: "premise host",
		Status: "unverified", ClaimVersion: 1, EvidenceIDs: []string{e.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	in, err := s.CreateIntent(model.Intent{Title: "parked direction", FromFactIDs: []string{f.ID}})
	if err != nil {
		t.Fatal(err)
	}
	in, err = s.UpdateIntent(in.ID, func(i *model.Intent) {
		i.Status = model.StatusParked
		i.Hold = true
		i.ParkedPremiseFactIDs = []string{f.ID}
		i.ParkedPremiseClaims = map[string]uint64{f.ID: 1}
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AdvanceCommanderAttention(s.CommanderAttention().Through); err != nil {
		t.Fatal(err)
	}

	if _, err := s.ReviewFact(model.Review{FactID: f.ID, Verdict: "pass", VerifierRun: "r_verify"}); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range s.CommanderAttention().Reasons {
		if r.Code == "parked_premise_ready" && r.IntentID == in.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("parked premise readiness was not raised: %+v", s.CommanderAttention().Reasons)
	}
}

func TestAttentionCheckpointSurvivesRestartAndDoesNotConsumeNewEvents(t *testing.T) {
	s := open(t)
	g1, _ := s.AddGoal(model.Goal{Statement: "first"})
	batch := s.CommanderAttention()
	g2, _ := s.AddGoal(model.Goal{Statement: "later"})
	if err := s.AdvanceCommanderAttention(batch.Through); err != nil {
		t.Fatal(err)
	}
	path := s.Path()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	a := reopened.CommanderAttention()
	if len(a.Reasons) != 1 || a.Reasons[0].ID != g2.ID || a.Reasons[0].ID == g1.ID {
		t.Fatalf("restart lost or replayed consumed event: %+v", a)
	}
}

func TestDiscoveryWithAnExplicitConsumerStaysLocal(t *testing.T) {
	s := open(t)
	f, _ := s.AddFact(model.Fact{Title: "shared"})
	in, _ := s.CreateIntent(model.Intent{Title: "consumer", FromFactIDs: []string{f.ID}})
	_, _ = s.UpdateIntent(in.ID, func(i *model.Intent) { i.Status = model.StatusRunning })
	if err := s.AdvanceCommanderAttention(s.CommanderAttention().Through); err != nil {
		t.Fatal(err)
	}
	_, _ = s.UpdateFact(f.ID, func(f *model.Fact) { f.Summary = "changed observation" })
	if a := s.CommanderAttention(); len(a.Reasons) != 0 {
		t.Fatalf("explicitly owned fact bought a global round: %+v", a)
	}
}

func TestSuccessfulCommanderGoalCreationDoesNotWakeItself(t *testing.T) {
	s := open(t)
	source, _, err := s.AddMessage(model.Message{FromParticipantID: collab.OperatorID, ToParticipantID: collab.CommanderID, Text: "operator goal change", Durable: true, Status: model.MessageConfirmed})
	if err != nil {
		t.Fatal(err)
	}
	run, _ := s.AddRun(model.Run{Purpose: "commander"})
	_, _ = s.AddGoal(model.Goal{Statement: "created by this round", CreatedByRun: run.ID, InstructionRef: source.ID})
	_, _ = s.UpdateRun(run.ID, func(r *model.Run) { r.Status = model.RunDone })
	if a := s.CommanderAttention(); len(a.Reasons) != 0 {
		t.Fatalf("commander woke on its own successful goal creation: %+v", a)
	}
	failed, _ := s.AddRun(model.Run{Purpose: "commander"})
	_, _ = s.AddGoal(model.Goal{Statement: "partially created before API failure", CreatedByRun: failed.ID, InstructionRef: source.ID})
	_, _ = s.UpdateRun(failed.ID, func(r *model.Run) { r.Status = model.RunFailed })
	if a := s.CommanderAttention(); len(a.Reasons) == 0 {
		t.Fatal("partial failed planning was silently consumed")
	}
}

func TestAttentionCheckpointFailureDoesNotConsumeEvents(t *testing.T) {
	s := open(t)
	_, _ = s.AddGoal(model.Goal{Statement: "pending"})
	a := s.CommanderAttention()
	if _, err := s.sqlite.db.Exec("CREATE TRIGGER fail_attention BEFORE UPDATE ON meta BEGIN SELECT RAISE(ABORT,'fixture checkpoint failure'); END"); err != nil {
		t.Fatal(err)
	}
	if err := s.AdvanceCommanderAttention(a.Through); err == nil {
		t.Fatal("checkpoint failure was swallowed")
	}
	if got := s.CommanderAttention(); got.DeliveredThrough != 0 || len(got.Reasons) == 0 {
		t.Fatal("failed checkpoint lost demand")
	}
}

func TestNativeSchedulingReplayIsFencedPerObserverAcrossRestart(t *testing.T) {
	s := open(t)
	if err := s.NoteCommanderNativeMailAt("a1", "host-a", 10); err != nil {
		t.Fatal(err)
	}
	if err := s.NoteCommanderNativeMailAt("b1", "host-b", 5); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteCommanderAttention(s.CommanderAttention()); err != nil {
		t.Fatal(err)
	}
	path := s.Path()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	// The observer crashed before saving its receipt, after another host's
	// mail was already consumed. Neither replay may create a new model round.
	for _, seq := range []int64{10, 9} {
		if err := s.NoteCommanderNativeMailAt("a1", "host-a", seq); err != nil {
			t.Fatal(err)
		}
	}
	if a := s.CommanderAttention(); len(a.Reasons) != 0 {
		t.Fatalf("consumed native mail replayed: %+v", a)
	}
	if err := s.NoteCommanderNativeMailAt("a2", "host-a", 11); err != nil {
		t.Fatal(err)
	}
	if a := s.CommanderAttention(); len(a.Reasons) != 1 {
		t.Fatalf("new native mail was lost: %+v", a)
	}
}

func TestNativeSchedulingCommitFailureDoesNotAdvanceObserver(t *testing.T) {
	s := open(t)
	defer s.Close()
	if _, err := s.sqlite.db.Exec("CREATE TRIGGER fail_native BEFORE UPDATE ON meta BEGIN SELECT RAISE(ABORT,'fixture native failure'); END"); err != nil {
		t.Fatal(err)
	}
	if err := s.NoteCommanderNativeMailAt("a1", "host-a", 1); err == nil {
		t.Fatal("failed scheduling transaction reported success")
	}
	if _, err := s.sqlite.db.Exec("DROP TRIGGER fail_native"); err != nil {
		t.Fatal(err)
	}
	if err := s.NoteCommanderNativeMailAt("a1", "host-a", 1); err != nil {
		t.Fatal(err)
	}
	if a := s.CommanderAttention(); len(a.Reasons) != 1 {
		t.Fatalf("failed transaction consumed notification: %+v", a)
	}
}

func TestNativeSchedulingSurvivesRestartWithoutCopyingMail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.NoteCommanderNativeMail("native-1"); err != nil {
		t.Fatal(err)
	}
	first := s.CommanderAttention()
	if err = s.NoteCommanderNativeMail("native-1"); err != nil {
		t.Fatal(err)
	}
	if s.CommanderAttention().NativeThrough != first.NativeThrough {
		t.Fatal("duplicate native signal advanced")
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if got := s.CommanderAttention(); len(got.Reasons) != 1 || got.Reasons[0].Code != "native_request" {
		t.Fatal("restart lost native demand")
	}
	if err = s.NoteCommanderNativeMail("native-2"); err != nil {
		t.Fatal(err)
	}
	if err = s.CompleteCommanderAttention(first); err != nil {
		t.Fatal(err)
	}
	if got := s.CommanderAttention(); len(got.Reasons) != 1 || got.Reasons[0].ID != "native-2" {
		t.Fatal("old round consumed new native mail")
	}
	if err = s.CompleteCommanderAttention(s.CommanderAttention()); err != nil {
		t.Fatal(err)
	}
	if got := s.CommanderAttention(); len(got.Reasons) != 0 {
		t.Fatal("native signal not completed")
	}
	if len(s.Messages(0)) != 0 || len(s.ListFacts("", 0)) != 0 {
		t.Fatal("native signal copied mailbox contents")
	}
}

// Parking is a decision, not an open to-do: the attention item must say so and
// carry the recorded reason instead of a generic "direction ended" line.
func TestParkedAttentionCarriesTheReason(t *testing.T) {
	s := open(t)
	in, err := s.CreateIntent(model.Intent{Title: "EyouCMS WAF 编码层绕过"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateIntent(in.ID, func(i *model.Intent) {
		i.Status = model.StatusParked
		i.Hold = true
		i.ParkReason = "测定完成：全阴性，不再重开"
	}); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range s.CommanderAttention().Reasons {
		if r.IntentID == in.ID && r.Code == "unowned_result" {
			found = strings.Contains(r.Detail, "不是待办") && strings.Contains(r.Detail, "不再重开")
			if !found {
				t.Fatalf("parked attention detail = %q", r.Detail)
			}
		}
	}
	if !found {
		t.Fatalf("no parked attention for %s: %+v", in.ID, s.CommanderAttention().Reasons)
	}
}
