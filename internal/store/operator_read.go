package store

import (
	"github.com/kksty/saker/internal/collab"
	"github.com/kksty/saker/internal/model"
	"time"
)

// MarkOperatorMessagesRead acknowledges only the console recipient, never a
// model or executor's inbox. One durable cursor covers an explicitly viewed page.
func (s *Store) MarkOperatorMessagesRead(through uint64) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	count := 0
	for i := range s.db.Messages {
		m := &s.db.Messages[i]
		if m.ToParticipantID == collab.OperatorID && m.Seq <= through && m.AckedAt == nil {
			m.Status = model.MessageConfirmed
			m.AckedAt = &now
			m.UpdatedAt = now
			count++
		}
	}
	if count == 0 {
		return 0, nil
	}
	if err := s.flushNoRev("message"); err != nil {
		return 0, err
	}
	return count, nil
}
