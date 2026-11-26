package retrieval

import (
	"fmt"
	"sort"
	"strings"

	"github.com/kksty/saker/internal/model"
	"github.com/kksty/saker/internal/store"
)

type PlaybookQuery struct {
	Q        string `json:"q,omitempty"`
	Tag      string `json:"tag,omitempty"`
	Status   string `json:"status,omitempty"`
	Archived string `json:"archived,omitempty"`
	Cursor   string `json:"-"`
	Limit    int    `json:"-"`
}
type PlaybookPage struct {
	Playbooks []model.Playbook `json:"playbooks"`
	Counts    map[string]int   `json:"counts"`
	Count     int              `json:"count"`
	PageInfo
}

func Playbooks(s *store.Store, q PlaybookQuery) (PlaybookPage, error) {
	if q.Archived != "" && q.Archived != "exclude" && q.Archived != "include" && q.Archived != "only" {
		return PlaybookPage{}, fmt.Errorf("invalid archived filter")
	}
	key := queryKey(struct {
		Type  string
		Query PlaybookQuery
	}{"playbooks", q})
	db, c, err := read(s, q.Cursor, key)
	if err != nil {
		return PlaybookPage{}, err
	}
	rows := []model.Playbook{}
	counts := map[string]int{}
	facts := model.FactIndex(db.Facts)
	for _, p := range db.Playbooks {
		if q.Archived == "only" && !p.Archived || q.Archived != "only" && q.Archived != "include" && p.Archived {
			continue
		}
		// The stored Status is the writer's declaration; the list, its counts and
		// its filter all use the same derived label so a row cannot be a candidate
		// in the header and confirmed in the table (or the reverse). The source
		// judgement is attached before the label moves: a stored confirmation
		// whose claim changed is still shown as "来源已变", not silently relabelled.
		p = p.WithSourceState(facts)
		if !p.Confirmed(facts) && p.Status != model.PlaybookCandidate && p.Status != model.PlaybookRetired {
			p.Status = model.PlaybookCandidate
		}
		if _, ok := TextScore(q.Q, p.ID+" "+p.Title, p.When, p.Doc+" "+strings.Join(p.Tags, " ")); !ok {
			continue
		}
		if q.Tag != "" {
			found := false
			for _, tag := range p.Tags {
				if strings.EqualFold(tag, q.Tag) {
					found = true
				}
			}
			if !found {
				continue
			}
		}
		counts[p.Status]++
		if q.Status == "" || p.Status == q.Status {
			rows = append(rows, p)
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		// Confirmed entries lead, and "confirmed" is the same derived label as
		// above — never the raw declaration.
		aConfirmed, bConfirmed := a.Confirmed(facts), b.Confirmed(facts)
		if aConfirmed != bConfirmed {
			return aConfirmed
		}
		if !a.UpdatedAt.Equal(b.UpdatedAt) {
			return a.UpdatedAt.After(b.UpdatedAt)
		}
		return a.ID < b.ID
	})
	out := PlaybookPage{Playbooks: []model.Playbook{}, Counts: counts, PageInfo: PageInfo{Total: len(rows), Version: c.Version}}
	if c.Offset > len(rows) {
		return out, fmt.Errorf("invalid_cursor")
	}
	end := c.Offset + pageLimit(q.Limit)
	if end > len(rows) {
		end = len(rows)
	}
	out.Playbooks = rows[c.Offset:end]
	out.Count = len(out.Playbooks)
	out.HasMore = end < len(rows)
	if out.HasMore {
		c.Offset = end
		out.NextCursor = encode(c)
	}
	return out, nil
}
