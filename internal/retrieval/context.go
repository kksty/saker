package retrieval

import (
	"encoding/json"

	"github.com/kksty/saker/internal/model"
	"github.com/kksty/saker/internal/store"
)

type Context struct {
	DeliveryID  string             `json:"delivery_id"`
	Changes     DeliveryChangePage `json:"changes"`
	Survey      FactPage           `json:"survey"`
	Impacts     []Impact           `json:"impacts"`
	Acknowledge string             `json:"acknowledge"`
}

func projectSessionDelivery(s *store.Store, out *Context) {
	s.ProjectSessionFacts(out.Survey.Facts)
	for i := range out.Changes.Changes {
		// Changes carry field names. Discard any values in an older saved payload.
		out.Changes.Changes[i].Value = nil
	}
}

// DeliveryChange is the change record the prompt delivery carries: identity,
// what happened, and — only when it is news — which fields changed.
//
// The ledger's own envelope is per-write bookkeeping: `transaction`, `at` and
// `position` say how and when the write was stored, and a `create` change's
// `fields` list is the row schema repeated verbatim for every row. A measured
// delivery of 20 evidence creates spent ~4 KB on that: the same ten column
// names 20 times, plus a transaction id and a timestamp nothing downstream can
// act on. `GET /api/changes` still returns the full store.Change record.
//
// An `update` keeps its field list: there it is the actual news (which column
// changed) rather than a restatement of the schema.
type DeliveryChange struct {
	Seq       uint64          `json:"seq"`
	Kind      string          `json:"kind"`
	ID        string          `json:"id"`
	Version   uint64          `json:"version"`
	Operation string          `json:"operation"`
	Fields    []string        `json:"fields,omitempty"`
	Value     json.RawMessage `json:"value,omitempty"`
}

// DeliveryChangePage keeps the pagination frame of ChangePage; only the record
// shape inside it is narrowed.
type DeliveryChangePage struct {
	Changes       []DeliveryChange  `json:"changes"`
	NextCursor    string            `json:"next_cursor"`
	HasMore       bool              `json:"has_more"`
	HighWatermark store.ReadVersion `json:"high_watermark"`
	Pending       int               `json:"pending"`
}

// deliveryChanges projects a ledger page into what the prompt needs. It never
// drops a row or reorders: seq ordering is what tells the model what happened
// after its last delivery.
func deliveryChanges(page ChangePage) DeliveryChangePage {
	out := DeliveryChangePage{
		Changes:       make([]DeliveryChange, 0, len(page.Changes)),
		NextCursor:    page.NextCursor,
		HasMore:       page.HasMore,
		HighWatermark: page.HighWatermark,
		Pending:       page.Pending,
	}
	for _, c := range page.Changes {
		dc := DeliveryChange{Seq: c.Seq, Kind: c.Kind, ID: c.ID, Version: c.Version, Operation: c.Operation}
		if c.Operation != "create" {
			dc.Fields = c.Fields
		}
		out.Changes = append(out.Changes, dc)
	}
	return out
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// A delivery preserves its ID and cursors until explicitly acknowledged. Its
// fact body visibility is checked again on each presentation.
func WorkingContext(s *store.Store, name string) (Context, error) {

	if pending, ok := s.PendingDelivery(name); ok {
		var out Context
		e := json.Unmarshal(pending.Payload, &out)
		if e == nil {
			projectSessionDelivery(s, &out)
		}
		return out, e
	}
	checkpoint := s.ReadCheckpoint(name)
	surveyHash := s.ContentHash()
	changes, e := Changes(s, checkpoint.Changes, 20, 0)
	if e != nil {
		return Context{}, e
	}
	survey := FactPage{Facts: []model.Fact{}}
	if !checkpoint.ScanComplete || checkpoint.SurveyHash != surveyHash {
		survey, e = Facts(s, FactQuery{Ascending: true, Cursor: checkpoint.Scan, Limit: 10})
	}
	if e != nil {
		return Context{}, e
	}
	for i := range survey.Facts {
		f := &survey.Facts[i]
		f.Doc = ""
		f.Title = clip(f.Title, 160)
		f.Summary = clip(f.Summary, 240)
	}
	db, _ := s.ReadView()
	factIDs := []string{}
	seen := map[string]bool{}
	for _, c := range changes.Changes {
		if c.Kind == "fact" && !seen[c.ID] {
			factIDs = append(factIDs, c.ID)
			seen[c.ID] = true
		}
	}
	out := Context{DeliveryID: store.NewID("delivery"), Changes: deliveryChanges(changes), Survey: survey, Impacts: Impacts(db, factIDs), Acknowledge: "POST /api/context/ack with delivery_id after processing or explicitly retaining the references in state"}
	projectSessionDelivery(s, &out)
	for i := range out.Impacts {
		v := &out.Impacts[i]
		v.IntentsTotal = len(v.Intents)
		v.GoalsTotal = len(v.Goals)
		v.Query = "/api/graph?root=fact:" + v.Fact + "&direction=descendants&depth=0"
		if len(v.Intents) > 5 {
			v.Intents = v.Intents[:5]
		}
		if len(v.Goals) > 5 {
			v.Goals = v.Goals[:5]
		}
	}
	raw, e := json.Marshal(out)
	if e != nil {
		return out, e
	}
	saved, e := s.SaveDelivery(name, store.Delivery{ID: out.DeliveryID, Expected: checkpoint, Next: store.Checkpoint{Changes: changes.NextCursor, ChangesPending: changes.HasMore, Scan: survey.NextCursor, ScanComplete: !survey.HasMore, SurveyHash: surveyHash}, Payload: raw})
	if e != nil {
		return out, e
	}
	e = json.Unmarshal(saved.Payload, &out)
	if e == nil {
		projectSessionDelivery(s, &out)
	}
	return out, e
}
