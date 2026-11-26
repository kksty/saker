package store

import (
	"errors"
	"fmt"
	"testing"

	"github.com/kksty/saker/internal/model"
)

// A process that dies with a claimed (sending) row must put it back on the
// queue: no byte was confirmed to leave, and no other path would ever pick the
// row up again. The [重投] marker is what makes the possible duplicate visible.
func TestRestartRequeuesInFlightMessage(t *testing.T) {
	s := open(t)
	m, _, err := s.AddMessage(model.Message{FromParticipantID: "a", ToParticipantID: "b", Text: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.BeginMessageDelivery(m.ID); err != nil || !ok {
		t.Fatalf("begin=%v %v", ok, err)
	}
	reopened, err := Open(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	if err := reopened.RecoverMessageDeliveries(); err != nil {
		t.Fatal(err)
	}
	got, ok := reopened.MessageByID(m.ID)
	if !ok {
		t.Fatal("message vanished across restart")
	}
	if got.Status != model.MessageQueued || got.Redeliveries != 1 {
		t.Fatalf("recovered row = %+v, want queued with one redelivery", got)
	}
	if got.Error == "" {
		t.Fatal("recovered row does not say why it was requeued")
	}
	pending := reopened.PendingFor("b")
	if len(pending) != 1 || pending[0].ID != m.ID {
		t.Fatalf("pending after restart = %+v, want the in-flight message", pending)
	}
}

// A transport error leaves the row unknown. The sender's retry with the same
// idempotency key is the explicit "send it again", so the answer must be a
// requeue, not created=false with the dead row untouched.
func TestSameKeyRetryRequeuesUnknownMessage(t *testing.T) {
	s := open(t)
	request := func() model.Message {
		return model.Message{FromParticipantID: "a", ToParticipantID: "b", Text: "hello", IdempotencyKey: "k1"}
	}
	m, created, err := s.AddMessage(request())
	if err != nil || !created {
		t.Fatalf("add=%v %v", created, err)
	}
	if _, ok, err := s.BeginMessageDelivery(m.ID); err != nil || !ok {
		t.Fatalf("begin=%v %v", ok, err)
	}
	if _, err := s.FinishMessageDelivery(m.ID, "", "r_b", "ioa", "宿主超时，结果未知"); err != nil {
		t.Fatal(err)
	}
	dead, _ := s.MessageByID(m.ID)
	if dead.Status != model.MessageUnknown {
		t.Fatalf("transport error status = %q, want unknown", dead.Status)
	}
	if len(s.PendingFor("b")) != 0 {
		t.Fatal("an unknown delivery entered the queue without an explicit retry")
	}

	again, created, err := s.AddMessage(request())
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Fatal("retry created a second row")
	}
	if again.ID != m.ID {
		t.Fatalf("retry resolved to %s, want the original %s", again.ID, m.ID)
	}
	if again.Status != model.MessageQueued || again.Redeliveries != 1 {
		t.Fatalf("retry row = %+v, want queued with one redelivery", again)
	}
	if n := len(s.Snapshot().Messages); n != 1 {
		t.Fatalf("messages after retry = %d, want 1", n)
	}
	if pending := s.PendingFor("b"); len(pending) != 1 || pending[0].ID != m.ID {
		t.Fatalf("pending after retry = %+v", pending)
	}
}

// A receipt is proof of delivery. Queued and unknown rows were never handed to
// the session, and confirming one would remove it from the inbox while the
// sender believes it was read.
func TestAckRefusedBeforeDelivery(t *testing.T) {
	s := open(t)
	queued, _, err := s.AddMessage(model.Message{FromParticipantID: "a", ToParticipantID: "b", Text: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AckMessage(queued.ID); !errors.Is(err, ErrMessageNotDelivered) {
		t.Fatalf("ack of a queued message = %v, want ErrMessageNotDelivered", err)
	}
	if got, _ := s.MessageByID(queued.ID); got.Status != model.MessageQueued || got.AckedAt != nil {
		t.Fatalf("refused ack changed the row: %+v", got)
	}
	if len(s.PendingFor("b")) != 1 {
		t.Fatal("refused ack took the message out of the queue")
	}

	if _, ok, err := s.BeginMessageDelivery(queued.ID); err != nil || !ok {
		t.Fatalf("begin=%v %v", ok, err)
	}
	if _, err := s.FinishMessageDelivery(queued.ID, "d1", "r_b", "ioa", ""); err != nil {
		t.Fatal(err)
	}
	acked, err := s.AckMessage(queued.ID)
	if err != nil || acked.Status != model.MessageConfirmed || acked.AckedAt == nil {
		t.Fatalf("ack of a delivered message = %+v %v", acked, err)
	}
	if again, err := s.AckMessage(queued.ID); err != nil || again.Status != model.MessageConfirmed {
		t.Fatalf("second ack = %+v %v", again, err)
	}

	unknown, _, err := s.AddMessage(model.Message{FromParticipantID: "a", ToParticipantID: "b", Text: "two"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.BeginMessageDelivery(unknown.ID); err != nil || !ok {
		t.Fatalf("begin=%v %v", ok, err)
	}
	if _, err := s.FinishMessageDelivery(unknown.ID, "", "r_b", "ioa", "timeout"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AckMessage(unknown.ID); !errors.Is(err, ErrMessageNotDelivered) {
		t.Fatalf("ack of an unknown delivery = %v, want ErrMessageNotDelivered", err)
	}
}

// The console list must be able to say "there is more" instead of presenting a
// truncated tail as the whole conversation.
func TestMessagesPageReportsTotalBeforeTruncation(t *testing.T) {
	s := open(t)
	for i := 0; i < 3; i++ {
		if _, _, err := s.AddMessage(model.Message{FromParticipantID: "a", ToParticipantID: "b", Text: "hello"}); err != nil {
			t.Fatal(err)
		}
	}
	page, total := s.MessagesPage(2)
	if len(page) != 2 || total != 3 {
		t.Fatalf("page=%d total=%d, want 2 and 3", len(page), total)
	}
	all, total := s.MessagesPage(0)
	if len(all) != 3 || total != 3 {
		t.Fatalf("default page=%d total=%d, want 3 and 3", len(all), total)
	}
}

// The console default page used to stop at 200. A longer conversation must not
// be cut there without saying so, which is what total/truncated exist for.
func TestMessagesDefaultPageExceedsTwoHundred(t *testing.T) {
	s := open(t)
	for i := 0; i < 205; i++ {
		s.db.Messages = append(s.db.Messages, model.Message{ID: "m_" + string(rune('a'+i%26)), Seq: uint64(i + 1), Text: "hello"})
	}
	page, total := s.MessagesPage(0)
	if len(page) != 205 || total != 205 {
		t.Fatalf("default page=%d total=%d, want 205 and 205", len(page), total)
	}
}

// A transport-error row (unknown) is a dead letter until its sender retries with
// the same key; it must not fill the 512-row admission budget and 429 a live
// message to a recipient whose queue holds nothing deliverable. The console view
// still shows all three states, so the row stays visible.
func TestUnknownDeadLettersDoNotBlockAdmission(t *testing.T) {
	s := open(t)
	for i := 0; i < MaxQueuedMessages; i++ {
		s.db.Messages = append(s.db.Messages, model.Message{
			ID: fmt.Sprintf("m_dead_%03d", i), Seq: uint64(i + 1),
			FromParticipantID: "a", ToParticipantID: "b", Text: "transport timed out",
			Status: model.MessageUnknown,
		})
	}
	if _, created, err := s.AddMessage(model.Message{FromParticipantID: "a", ToParticipantID: "b", Text: "live message"}); err != nil || !created {
		t.Fatalf("live message blocked by dead letters: created=%v err=%v", created, err)
	}
	if deliverable := s.PendingFor("b"); len(deliverable) != 1 {
		t.Fatalf("deliverable queue = %d, want 1", len(deliverable))
	}
	if got := s.queueStatsLocked("b"); got.Pending != MaxQueuedMessages+1 {
		t.Fatalf("console queue view = %d, want %d (dead letters stay visible)", got.Pending, MaxQueuedMessages+1)
	}
}

// MessagesPageWhere counts and pages over the rows the filter admits: the page is
// a tail of the filtered set, and total is that set's size, not the project's.
func TestMessagesPageWhereFiltersBeforePage(t *testing.T) {
	s := open(t)
	for i, from := range []string{"a", "b", "a"} {
		s.db.Messages = append(s.db.Messages, model.Message{
			ID: fmt.Sprintf("m_%d", i), Seq: uint64(i + 1),
			FromParticipantID: from, ToParticipantID: "c", Text: "hello",
		})
	}
	page, total := s.MessagesPageWhere(2, func(m model.Message) bool { return m.FromParticipantID == "a" })
	if len(page) != 2 || total != 2 {
		t.Fatalf("filtered page=%d total=%d, want 2 and 2", len(page), total)
	}
	all, totalAll := s.MessagesPageWhere(2, nil)
	if len(all) != 2 || totalAll != 3 {
		t.Fatalf("unfiltered page=%d total=%d, want 2 and 3", len(all), totalAll)
	}
}

