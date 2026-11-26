package store

import (
	"errors"
	"testing"
	"time"

	"github.com/kksty/saker/internal/model"
)

// A receipt belongs to the delivery attempt it confirms. A late ack from the
// previous session must not confirm a redelivery, a repeated ack returns the
// original receipt, and an ack that arrives before the transport call returns is
// legal and keeps its first confirmation time.
func TestMessageAckBindsCurrentDeliveryRound(t *testing.T) {
	s := open(t)
	msg, _, err := s.AddMessage(model.Message{FromParticipantID: "a", ToParticipantID: "b", Text: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.BeginMessageDelivery(msg.ID, "r_a"); err != nil || !ok {
		t.Fatalf("begin=%v %v", ok, err)
	}
	if _, err := s.AckMessageDelivery(msg.ID, "", "r_b"); !errors.Is(err, ErrMessageDeliveryMismatch) {
		t.Fatalf("ack from another run = %v, want mismatch", err)
	}
	if _, err := s.FinishMessageDelivery(msg.ID, "d1", "r_a", "ioa", ""); err != nil {
		t.Fatal(err)
	}
	first, err := s.AckMessageDelivery(msg.ID, "d1", "r_a")
	if err != nil || first.Status != model.MessageConfirmed || first.AckedAt == nil {
		t.Fatalf("first ack = %+v %v", first, err)
	}
	firstAt := *first.AckedAt
	time.Sleep(5 * time.Millisecond)
	again, err := s.AckMessageDelivery(msg.ID, "d1", "r_a")
	if err != nil {
		t.Fatal(err)
	}
	if again.AckedAt == nil || !again.AckedAt.Equal(firstAt) {
		t.Fatalf("repeated ack moved the first confirmation time: %v -> %v", firstAt, again.AckedAt)
	}
	if again.AckedRunID != "r_a" || again.AckedDeliveryID != "d1" {
		t.Fatalf("receipt binding = run %q delivery %q", again.AckedRunID, again.AckedDeliveryID)
	}

	// Requeue the same message for a new session, then try to confirm it with
	// the previous run's identity.
	other, _, err := s.AddMessage(model.Message{FromParticipantID: "a", ToParticipantID: "b", Text: "second"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.BeginMessageDelivery(other.ID, "r_old"); !ok {
		t.Fatal("old delivery not claimed")
	}
	if _, err := s.FinishMessageDelivery(other.ID, "d_old", "r_old", "ioa", ""); err != nil {
		t.Fatal(err)
	}
	if n, err := s.RedeliverAllUnconfirmed("b"); err != nil || n != 1 {
		t.Fatalf("requeue unconfirmed = %d %v", n, err)
	}
	if _, ok, _ := s.BeginMessageDelivery(other.ID, "r_new"); !ok {
		t.Fatal("redelivery not claimed")
	}
	if _, err := s.FinishMessageDelivery(other.ID, "d_new", "r_new", "ioa", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AckMessageDelivery(other.ID, "d_new", "r_old"); !errors.Is(err, ErrMessageDeliveryMismatch) {
		t.Fatalf("late ack from the previous run = %v, want mismatch", err)
	}
	current, err := s.AckMessageDelivery(other.ID, "d_new", "r_new")
	if err != nil || current.AckedRunID != "r_new" || current.AckedDeliveryID != "d_new" {
		t.Fatalf("current ack = %+v %v", current, err)
	}
	if len(current.Deliveries) != 2 {
		t.Fatalf("delivery history = %+v, want two attempts", current.Deliveries)
	}
	if current.Deliveries[0].RunID != "r_old" || current.Deliveries[1].RunID != "r_new" {
		t.Fatalf("delivery history lost the rounds: %+v", current.Deliveries)
	}

	// Early receipt: the recipient answers before FinishMessageDelivery returns.
	early, _, err := s.AddMessage(model.Message{FromParticipantID: "a", ToParticipantID: "b", Text: "early"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.BeginMessageDelivery(early.ID, "r_early"); !ok {
		t.Fatal("early delivery not claimed")
	}
	ack, err := s.AckMessageDelivery(early.ID, "", "r_early")
	if err != nil || ack.Status != model.MessageConfirmed || ack.AckedAt == nil {
		t.Fatalf("early ack = %+v %v", ack, err)
	}
	earlyAt := *ack.AckedAt
	if _, err := s.FinishMessageDelivery(early.ID, "d_early", "r_early", "ioa", ""); err != nil {
		t.Fatal(err)
	}
	settled, _ := s.MessageByID(early.ID)
	if settled.Status != model.MessageConfirmed || settled.AckedAt == nil || !settled.AckedAt.Equal(earlyAt) {
		t.Fatalf("finish undid or moved the early receipt: %+v", settled)
	}
	if settled.AckedDeliveryID != "d_early" {
		t.Fatalf("early receipt did not learn its transport id: %+v", settled)
	}
}
