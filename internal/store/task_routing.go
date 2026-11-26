package store

import (
	"github.com/kksty/saker/internal/model"
	"sort"
)

func (s *Store) PendingCoordinationNotices() []model.Message {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []model.Message{}
	for _, m := range s.db.Messages {
		if m.Status == model.MessageQueued && m.NoticeFactID != "" {
			m.FactRefs = append([]string(nil), m.FactRefs...)
			out = append(out, m)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	return out
}
