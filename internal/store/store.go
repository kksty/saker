// Package store is Tianwu's ledger persistence.
//
// All project ledgers use embedded SQLite. JSON is only a portable export format.
package store

import (
	"crypto/rand"
	"encoding/base32"
	"errors"
	"fmt"
	"log"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/kksty/tianwu/internal/model"
)

type DB struct {
	Notebooks     []model.Notebook        `json:"notebooks,omitempty"`
	Routes        []model.RouteCheckpoint `json:"routes,omitempty"`
	Instructions  []model.Instruction     `json:"instructions,omitempty"`
	ReadEdges     []Relation              `json:"-"`
	ReadExistence map[string]string       `json:"-"`
	ReadRevision  uint64                  `json:"-"`
	ContentHash   string                  `json:"-"`
	Memory        *Memory                 `json:"memory,omitempty"`
	Actions       []model.ExternalAction  `json:"actions,omitempty"`
	Version       int                     `json:"version"`
	Intents       []model.Intent          `json:"intents"`
	Facts         []model.Fact            `json:"facts"`
	Evidence      []model.Evidence        `json:"evidence"`
	Goals         []model.Goal            `json:"goals"`
	Reviews       []model.Review          `json:"reviews"`
	Runs          []model.Run             `json:"runs"`
	// Playbooks is project knowledge, not findings: a reusable tactic with
	// provenance but no evidence and no review lifecycle. It is a table in the
	// same snapshot because the ledger is one atomic state, not because a
	// playbook is a fact — nothing in the fact paths (board counts, report,
	// verification) reads this field (D70).
	Playbooks []model.Playbook `json:"playbooks,omitempty"`
	// OperatorRequests are project-level asks for the operator (authorization,
	// blocked next step). They are control state, not findings: board/report/
	// review never read them, and they do not enter the content digest.
	OperatorRequests []model.OperatorRequest `json:"operator_requests,omitempty"`
	// Messages are participant-to-participant conversation. They share the
	// atomic snapshot because idempotency and delivery state must survive a
	// restart with the rest of the project, but they are not facts: board,
	// report, review and coverage never read this field.
	Messages []model.Message `json:"messages,omitempty"`
	// MessageSeq is the read cursor's high-water mark. It is stored instead of
	// derived from the rows because a withdrawn or swept message is deleted: a
	// maximum over the surviving rows would rewind and a reader's `after`
	// cursor would then skip the next message (D86).
	MessageSeq uint64 `json:"message_seq,omitempty"`

	// Documents are the three project documents the prompts are built from.
	// Each save is a new revision of the same row, so the ledger's version
	// history is the audit trail; no second table stores their text.
	Documents []model.Document `json:"documents,omitempty"`
}

type Store struct {
	sqlite         *sqliteBackend
	records        map[string]record
	recordKinds    map[string]map[string]record
	readCacheMu    sync.Mutex
	readCache      map[uint64]cachedRead
	readCacheOrder []uint64
	readCacheBytes int
	readEpoch      string
	mu             sync.RWMutex
	path           string
	db             DB
	// rev counts durable writes. It is the decider's "the world changed" signal
	// for edits no count can see: a fact's title/doc/summary rewritten, a ref
	// added, a row changed while it sat outside the board's top-N (D79). Live
	// pulse touches (NoteEvent, ProgressRun) deliberately do not bump it — those
	// are "still alive", not news, and waking on them would burn a round per
	// event.
	rev uint64
	// Committed metadata shares copy-on-write Memory. The record indexes own
	// the committed row bytes independently of mutable working rows in db.
	committedRev  uint64
	committedMeta sqliteMeta
	// onFact is called after a write, outside the lock. The instance uses it to
	// mirror vulnerabilities into the global library; the store itself stays
	// project-scoped and knows nothing about the library.
	onFact func(model.Fact, []string)
	// changeReason/changeReasonIDs label the version-journal entries of one
	// transaction: dependency propagation has to say which rows lost their
	// backing and to which premise. They are set only across a single flush (see
	// flushFactChangesLocked) and never survive it.
	changeReason    string
	changeReasonIDs map[string]bool
}

// OnFact registers a mirror hook. It runs with the store unlocked, after the
// write is durable: a hook that reads this store would deadlock otherwise.
func (s *Store) OnFact(fn func(model.Fact, []string)) {
	s.mu.Lock()
	s.onFact = fn
	s.mu.Unlock()
}

// evidenceURIsLocked maps evidence ids to case-relative URIs. Caller holds the
// lock.
func (s *Store) evidenceURIsLocked(ids []string) []string {
	if len(ids) == 0 {
		return nil
	}
	byID := make(map[string]string, len(s.db.Evidence))
	for _, e := range s.db.Evidence {
		byID[e.ID] = e.URI
	}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if uri, ok := byID[id]; ok {
			out = append(out, uri)
		}
	}
	return out
}

// schemaVersion identifies the current SQLite layout; older layouts are unsupported.
const schemaVersion = 13

// CurrentSchemaVersion is the layout this build writes and accepts. It is
// exported for fixtures that build a ledger snapshot directly: a hard-coded
// number in a test is a silent schema claim that stops being true on the next
// bump.
func CurrentSchemaVersion() int { return schemaVersion }

func Open(path string) (*Store, error) { return OpenSQLite(path) }

// NewCode returns n lowercase base32 characters (5 random bits each) from the
// system entropy source. Identifiers are the only consumer: each caller owns its
// uniqueness rule (uniqueIDLocked inside a ledger, Manager.uniqueID across
// project directories), because a random code alone cannot promise it.
func NewCode(n int) string {
	if n <= 0 {
		return ""
	}
	if n > 12 {
		n = 12
	}
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("ledger identity entropy unavailable: " + err.Error())
	}
	// 8 bytes encode to 13 base32 characters, enough for the 12-character cap.
	code := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b[:])
	return strings.ToLower(code[:n])
}

// NewID is a record identity: prefix + "_" + 7 base32 characters (35 random
// bits). Portability matters more than length here: an imported ledger keeps its
// historical ids, which may encode a different width, and nothing parses them.
func NewID(prefix string) string {
	return prefix + "_" + NewCode(7)
}

// Call with the store lock held. Short identities must never overwrite a
// surviving imported record, even in the unlikely event of a random collision.
func (s *Store) uniqueIDLocked(prefix string) string {
	for {
		id := NewID(prefix)
		exists := false
		for kind := range s.recordKinds {
			if _, ok := s.records[recordKey(kind, id)]; ok {
				exists = true
				break
			}
		}
		if !exists {
			return id
		}
	}
}

// flush must be called with the write lock held.
//
// Affected rows, revisions, indexes and metadata commit in one SQLite
// transaction. A failed commit restores the last committed in-memory state.
func (s *Store) flush(kinds ...string) error {
	s.rev++
	return s.flushNoRev(kinds...)
}

// flushNoRev persists without bumping the decider's change counter. Messages
// use it: they are durable project state, but a chat line is not a change to the
// fact/intent graph, and waking the commander once per message would burn a
// model round for nothing (D79 defines rev as that wake signal, not a raw write
// count).
func (s *Store) flushNoRev(kinds ...string) error { return s.flushSQLite(kinds...) }

func (s *Store) CreateIntent(in model.Intent) (model.Intent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.applyInitialContextLocked(&in); err != nil {
		return model.Intent{}, err
	}
	s.snapshotPremiseClaimsLocked(&in)
	now := time.Now()
	in.ID = s.uniqueIDLocked("in")
	in.Status = model.StatusPending
	in.CreatedAt, in.UpdatedAt = now, now
	in = detachIntent(in)
	s.db.Intents = append(s.db.Intents, in)
	return detachIntent(in), s.flush("intent")
}

func (s *Store) ListIntents() []model.Intent {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]model.Intent, len(s.db.Intents))
	for i, in := range s.db.Intents {
		out[i] = detachIntent(in)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}

func (s *Store) GetIntent(id string) (model.Intent, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, in := range s.db.Intents {
		if in.ID == id {
			return detachIntent(in), true
		}
	}
	return model.Intent{}, false
}

// ClaimIntent atomically moves pending -> claimed. It is the only concurrency
// guard in v0: no lease, no expiry, no version. A dead worker is recovered by
// the watchdog resetting the intent to pending.
//
// A claim is also the end of an abort marking. Aborted describes how the run
// that was just cancelled should settle (failed, so a parent's wait unblocks);
// it is not a property of the direction forever. Keeping it sticky made every
// later cancellation of that direction settle as failed+Hold, so after one
// worker kill / subtree cancel a retried direction could never be steered
// again: steer cancels the run and waits for pending, and the stale flag sent
// it to failed instead. Every deliberate start path (run, retry, continue,
// wake) goes through this claim; the scheduler skips held intents, so a
// cancelled subtree cannot restart itself this way.
func (s *Store) ClaimIntent(id, by string, owner ...string) (model.Intent, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.db.Intents {
		in := &s.db.Intents[i]
		if in.ID != id {
			continue
		}
		if in.Status != model.StatusPending && in.Status != model.StatusFailed && in.Status != model.StatusReviewFailed {
			return detachIntent(*in), false, nil
		}
		if !handoffOwnerMatches(in, firstOwner(owner)) {
			return detachIntent(*in), false, ErrHandoffConflict
		}
		in.Status = model.StatusClaimed
		in.ClaimedBy = by
		in.Hold = false
		in.HeldBy, in.HoldReason = "", ""
		in.HandoffOperation = ""
		in.Aborted = false
		in.Attempts++
		in.UpdatedAt = time.Now()
		return detachIntent(*in), true, s.flush("intent")
	}
	return model.Intent{}, false, fmt.Errorf("intent %s not found", id)
}

// ReopenIntent moves a parked intent back to pending. Re-opening is explicit:
// a park is a decision, so undoing it is one too (docs/06, D9).
func (s *Store) ReopenIntent(id string) (model.Intent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.db.Intents {
		in := &s.db.Intents[i]
		if in.ID != id {
			continue
		}
		if in.Status != model.StatusParked {
			return detachIntent(*in), fmt.Errorf("intent %s 不是停放状态（%s）", id, in.Status)
		}
		in.Status = model.StatusPending
		in.ParkReason = ""
		in.ParkedPremiseFactIDs = nil
		in.ParkedPremiseClaims = nil
		in.ParkedAt = nil
		in.Hold = false
		in.HeldBy, in.HoldReason = "", ""
		// Reopening puts the direction back on the queue: a completion that a
		// previous turn declared is history now, not the current state.
		in.ClearDoneDeclared()
		in.UpdatedAt = time.Now()
		return detachIntent(*in), s.flush("intent")
	}
	return model.Intent{}, fmt.Errorf("intent %s not found", id)
}

func (s *Store) UpdateIntent(id string, fn func(*model.Intent)) (model.Intent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.db.Intents {
		if s.db.Intents[i].ID != id {
			continue
		}
		previous := detachIntent(s.db.Intents[i])
		fn(&s.db.Intents[i])
		row := detachIntent(s.db.Intents[i])
		if err := s.validateIntentStructureLocked(&row, previous); err != nil {
			s.db.Intents[i] = previous
			return model.Intent{}, err
		}
		s.db.Intents[i] = row
		s.db.Intents[i].UpdatedAt = time.Now()
		return detachIntent(s.db.Intents[i]), s.flush("intent")
	}
	return model.Intent{}, fmt.Errorf("intent %s not found", id)
}

// UpdateIntentIfCurrent prepares a lifecycle transition only for the run/status
// the caller inspected. Stale retries must not reset an already claimed run.
func (s *Store) UpdateIntentIfCurrent(id, status, runID string, fn func(*model.Intent)) (model.Intent, bool, error) {
	return s.updateIntentIfCurrent(id, status, runID, nil, fn)
}

// UpdateIntentIfHeld is UpdateIntentIfCurrent plus the hold state the caller
// inspected. A concurrent operator/commander stop changes Hold/HeldBy without
// touching status or runID; a restart that read the old row must not overwrite
// that decision. An unmatched hold is a lost CAS, not an error.
func (s *Store) UpdateIntentIfHeld(id, status, runID string, hold bool, heldBy string, fn func(*model.Intent)) (model.Intent, bool, error) {
	return s.updateIntentIfCurrent(id, status, runID, func(in *model.Intent) bool {
		return in.Hold == hold && in.HeldBy == heldBy
	}, fn)
}

func (s *Store) updateIntentIfCurrent(id, status, runID string, match func(*model.Intent) bool, fn func(*model.Intent)) (model.Intent, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.db.Intents {
		in := &s.db.Intents[i]
		if in.ID != id {
			continue
		}
		if in.Status != status || in.RunID != runID || (match != nil && !match(in)) {
			return detachIntent(*in), false, nil
		}
		previous := detachIntent(*in)
		fn(in)
		if err := s.validateIntentStructureLocked(in, previous); err != nil {
			return model.Intent{}, false, err
		}
		*in = detachIntent(*in)
		in.UpdatedAt = time.Now()
		out := detachIntent(*in)
		err := s.flush("intent")
		return out, err == nil, err
	}
	return model.Intent{}, false, fmt.Errorf("intent %s not found", id)
}

// UpdateIntentWhen runs fn only when allow accepts the row as it is under the
// write lock. Guards that depend on the caller's identity or on a state another
// writer can change between a read and a write (for example "may this caller
// release this hold?") must be evaluated here: checking a detached snapshot and
// then calling UpdateIntent would overwrite a stop that landed in between.
// A refused predicate is a lost race, not an error.
func (s *Store) UpdateIntentWhen(id string, allow func(model.Intent) bool, fn func(*model.Intent)) (model.Intent, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.db.Intents {
		in := &s.db.Intents[i]
		if in.ID != id {
			continue
		}
		if allow != nil && !allow(detachIntent(*in)) {
			return detachIntent(*in), false, nil
		}
		previous := detachIntent(*in)
		fn(in)
		if err := s.validateIntentStructureLocked(in, previous); err != nil {
			return model.Intent{}, false, err
		}
		*in = detachIntent(*in)
		in.UpdatedAt = time.Now()
		out := detachIntent(*in)
		err := s.flush("intent")
		return out, err == nil, err
	}
	return model.Intent{}, false, fmt.Errorf("intent %s not found", id)
}

func (s *Store) AddFact(f model.Fact) (model.Fact, error) {
	s.mu.Lock()
	f = detachFact(f)
	f.ID = s.uniqueIDLocked("f")
	if f.CreatedAt.IsZero() {
		f.CreatedAt = time.Now()
	}
	// A caller that only knows the declaration writes Confidence; that value is
	// the base, not the effective label. From here on the store owns the label.
	if f.DeclaredConfidence == "" {
		f.DeclaredConfidence = f.Confidence
	}
	if f.DeclaredConfidence != "inferred" {
		f.DeclaredConfidence = "observed"
	}
	// A row cannot be more certain than the premises of the direction that
	// produced it: one unverified hard premise makes the label inferred, whatever
	// the writer declared. A direction with no premises is unaffected — a first
	// touch rests on the authorization, not on another fact.
	s.deriveConfidenceLocked(&f)
	// A fact is never born verified. The verdict is the store's alone: only a
	// passing review bound to this claim version and hash can set it (ReviewFact).
	// A caller that hands in "verified" is asking for an endorsement nobody
	// recorded, and the report path reads that field as proof, so it is refused
	// here rather than discovered downstream. Imported ledgers do not pass
	// through this path: a bundle is a whole-database copy and carries its
	// reviews with it.
	if f.Status == "verified" {
		f.Status = "unverified"
	}
	if f.Status == "" {
		f.Status = "unverified"
	}
	// Unknown origin is treated as the least-trusted one: a forgotten call site
	// must not be able to inflate the board's declared count.
	if f.Origin == "" {
		f.Origin = model.OriginHeuristic
	}
	// A new row is the first version of its assertion. The fingerprint is
	// computed from the claim only, never from the row's review status or its
	// display title (see claimHash).
	f.ClaimVersion, f.ClaimHash = 1, claimHash(f)
	s.db.Facts = append(s.db.Facts, f)
	err := s.flush("fact")
	hook := s.onFact
	uris := s.evidenceURIsLocked(f.EvidenceIDs)
	s.mu.Unlock()

	if err == nil && hook != nil {
		hook(f, uris)
	}
	if err == nil {
		s.notifyFactChange()
	}
	return detachFact(f), err
}

// notifyFactChange runs one association-routing pass after a committed fact
// change. It is called without the write lock (RouteFactChanges takes it), and
// a routing failure is only logged: the fact commit stands, the change is
// durable in the journal, and the next pass resumes from each recipient's
// checkpoint, so a delayed notice is never a lost one.
func (s *Store) notifyFactChange() {
	if err := s.RouteFactChanges(64); err != nil {
		log.Printf("关联事实通知入队失败（事实已提交，游标未推进，下次重试）：%v", err)
	}
}

// FindFact is gone: matching explicit observations by (intent, kind, title)
// made two real observations with the same label collapse into one row. The
// remaining title identity belongs to a single derived note and lives in
// FindDerivedFact (claim.go); explicit declarations resolve through
// FindDeclaration by writer-supplied identity (D74).

// UpdateFact mutates an existing row and flushes. The ID and CreatedAt stay:
// a live fact that later gains evidence is the same fact, not a new one.
//
// Changing the claim itself (summary, doc, evidence, tags, proof, ...) starts a
// new assertion version and returns the row to unverified: an old verdict
// proved the old content. Bookkeeping-only writes (source run, declaration ref,
// display title) keep the version and the review status.
func (s *Store) UpdateFact(id string, fn func(*model.Fact)) (model.Fact, error) {
	s.mu.Lock()
	var out model.Fact
	var demoted []string
	demoteReason := ""
	found := false
	for i := range s.db.Facts {
		if s.db.Facts[i].ID != id {
			continue
		}
		s.db.Facts[i] = detachFact(s.db.Facts[i])
		previous := s.db.Facts[i]
		fn(&s.db.Facts[i])
		row := &s.db.Facts[i]
		// A declaration write may carry a new base; the store derives the label
		// from it and from the premises. A caller that writes Confidence is
		// treated as declaring a base only when it did not name one.
		if row.DeclaredConfidence == "" {
			row.DeclaredConfidence = previous.DeclaredConfidence
		}
		if row.DeclaredConfidence == "" {
			row.DeclaredConfidence = previous.Confidence
		}
		s.deriveConfidenceLocked(row)
		row.ClaimHash = claimHash(*row)
		claimChanged := row.ClaimHash != previous.ClaimHash
		if claimChanged {
			row.ClaimVersion = previous.ClaimVersion + 1
			// Same invariant as AddFact: a writer never hands out "verified".
			// The new assertion has no verdict yet, so the old pass (which proved
			// a different sentence) cannot carry over; only ReviewFact sets this
			// back. Keep this unconditional if more fields join the claim hash.
			row.Status = "unverified"
			// The withdrawal belonged to the old assertion. New content needs
			// its own review; carrying the retraction forward would let a
			// correction hide under an old "do not use" mark.
			row.RetractedAt, row.RetractedBy, row.RetractedRunID = nil, "", ""
			row.RetractionReason, row.RetractedClaimVersion = "", 0
		} else {
			row.ClaimVersion = previous.ClaimVersion
			// Status is not part of the claim hash, so nothing above can see it: a
			// caller could otherwise set verified on unchanged content and keep it.
			// Only ReviewFact introduces a verdict, so a writer asking for one gets it
			// back as unverified. A bookkeeping write that leaves an existing verdict
			// alone still passes through untouched.
			if row.Status == "verified" && previous.Status != "verified" {
				row.Status = "unverified"
			}
		}
		// A changed assertion stops backing what rested on the old one: a verdict
		// on v1 says nothing about v2. Same path as a withdrawal — the rows below
		// lose their endorsement and keep their authorship.
		if claimChanged || previous.Status == "verified" && row.Status != "verified" {
			demoted = s.refreshDownstreamLocked(id, true)
			if len(demoted) > 0 {
				demoteReason = premiseLostReason + id
				if claimChanged {
					demoteReason = premiseChangedReason + id
				}
			}
		}
		s.db.Facts[i] = detachFact(s.db.Facts[i])
		out = detachFact(s.db.Facts[i])
		found = true
		break
	}
	if !found {
		s.mu.Unlock()
		return model.Fact{}, fmt.Errorf("fact %s not found", id)
	}
	err := s.flushFactChangesLocked(demoteReason, demoted, "fact")
	hook := s.onFact
	uris := s.evidenceURIsLocked(out.EvidenceIDs)
	s.mu.Unlock()
	if err == nil && hook != nil {
		hook(out, uris)
	}
	if err == nil {
		s.notifyFactChange()
	}
	return out, err
}

// WithdrawFact marks the current assertion withdrawn. It is a state change on an
// existing row, never a delete: the id, the evidence and every earlier version
// stay readable, and the change itself enters the version journal.
//
// The actor rules live in the API (authoring direction or operator); the store
// enforces only what the ledger needs: a reason, an existing row, and the
// transition itself. Repeating a withdrawal is a no-op that returns the row, so
// a retried request cannot overwrite the first actor or reason.
func (s *Store) WithdrawFact(id, actor, runID, reason string) (model.Fact, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return model.Fact{}, fmt.Errorf("撤回必须写理由：说明为什么这条记录不应保留")
	}
	s.mu.Lock()
	var out model.Fact
	found, changed := false, false
	for i := range s.db.Facts {
		if s.db.Facts[i].ID != id {
			continue
		}
		f := &s.db.Facts[i]
		if f.Status == "withdrawn" {
			out = detachFact(*f)
			found = true
			break
		}
		now := time.Now()
		f.Status = "withdrawn"
		f.RetractedAt = &now
		f.RetractedBy = actor
		f.RetractedRunID = runID
		f.RetractionReason = reason
		f.RetractedClaimVersion = f.ClaimVersion
		s.db.Facts[i] = detachFact(*f)
		out = detachFact(s.db.Facts[i])
		found, changed = true, true
		break
	}
	if !found {
		s.mu.Unlock()
		return model.Fact{}, fmt.Errorf("fact %s not found", id)
	}
	if !changed {
		s.mu.Unlock()
		return out, nil
	}
	// Withdrawal removes the backing this row gave the rows below it. They lose
	// their endorsement, not their authorship: status goes back to unverified and
	// the journal says which premise went away.
	lost := s.refreshDownstreamLocked(id, true)
	err := s.flushFactChangesLocked(premiseLostReason+id, lost, "fact")
	hook := s.onFact
	uris := s.evidenceURIsLocked(out.EvidenceIDs)
	s.mu.Unlock()
	if err == nil && hook != nil {
		hook(out, uris)
	}
	if err == nil {
		s.notifyFactChange()
	}
	return out, err
}

func (s *Store) ListFacts(intentID string, limit int) []model.Fact {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]model.Fact, 0, len(s.db.Facts))
	for _, f := range s.db.Facts {
		if intentID == "" || f.IntentID == intentID {
			out = append(out, detachFact(f))
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

func (s *Store) AddEvidence(e model.Evidence) (model.Evidence, error) {
	e, err := normalizeEvidence(e)
	if err != nil {
		return model.Evidence{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// Replaying a committed declaration must keep its proof identity. The same
	// preserved bytes at the same source in one intent reuse their first
	// admission; a new capture time belongs to the fact's observation metadata.
	if e.Digest != "" && e.IntentID != "" {
		for _, old := range s.db.Evidence {
			if old.IntentID == e.IntentID && old.URI == e.URI && old.Digest == e.Digest &&
				old.SourceURI == e.SourceURI && old.From == e.From && old.Name == e.Name && old.Mime == e.Mime && old.Size == e.Size {
				return old, nil
			}
		}
	}
	e.ID = s.uniqueIDLocked("e")
	if e.CreatedAt.IsZero() {
		e.CreatedAt = time.Now()
	}
	s.db.Evidence = append(s.db.Evidence, e)
	return e, s.flush("evidence")
}

func (s *Store) AddRun(r model.Run) (model.Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r.ID = s.uniqueIDLocked("r")
	r.StartedAt = time.Now()
	r.Status = "running"
	r = detachRun(r)
	s.db.Runs = append(s.db.Runs, r)
	return detachRun(r), s.flush("run")
}

// NoteEvent updates the live pulse of a running run without flushing to disk.
// It is called for every executor event: the board needs to see activity, but
// a durable transaction per event would make the ledger the bottleneck. A
// later run write persists the pulse; finish replaces Events with the harness's
// authoritative count.
//
// Beyond the counter it keeps the one-line glance: the most recent *meaningful*
// event's kind/tool/text. Usage and delta events keep the counter moving but do
// not overwrite the glance — "still alive" is not "doing something", and a
// board that says "usage" on every run tells the decider nothing.
//
// The text is clipped here, at the write: this field rides in every board
// prompt, so it must stay a glance, not a transcript. The run_events tool
// carries the tail for the rounds that need to look closer (D57, D71).
func (s *Store) NoteEvent(id string, at time.Time, kind, tool, text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.db.Runs {
		if s.db.Runs[i].ID != id {
			continue
		}
		s.db.Runs[i].LastEventAt = &at
		s.db.Runs[i].Events++
		if kind == "turn_started" {
			// Persisted by the run's finish (which overwrites/flushes the row).
			// Redelivery uses it as "messages from this instant on may be
			// unread"; zero means the run never started a turn.
			s.db.Runs[i].LastTurnStartedAt = &at
		}
		switch kind {
		case "", "usage", "message_delta", "raw", "turn_started":
			// Keep the previous glance.
		default:
			s.db.Runs[i].LastEventKind = kind
			s.db.Runs[i].LastEventTool = tool
			s.db.Runs[i].LastEventText = clipEventText(text, 160)
		}
		break
	}
}

// ObserveRun records execution metadata separately from the human-readable pulse.
// Failures are transient until the next durable write, like NoteEvent. Session
// identity is flushed once so communication remains attributable after restart.
func (s *Store) ObserveRun(id, sessionID string, failed bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.db.Runs {
		r := &s.db.Runs[i]
		if r.ID != id {
			continue
		}
		if failed {
			r.ToolFailures++
		}
		if sessionID != "" && r.SessionID != sessionID {
			r.SessionID = sessionID
			return s.flush("run")
		}
		return nil
	}
	return fmt.Errorf("run %s not found", id)
}

func clipEventText(s string, n int) string {
	s = strings.TrimSpace(s)
	if n <= 0 {
		return s
	}
	r := []rune(s)
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n]) + "…"
}

func (s *Store) UpdateRun(id string, fn func(*model.Run)) (model.Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.db.Runs {
		if s.db.Runs[i].ID != id {
			continue
		}
		s.db.Runs[i] = detachRun(s.db.Runs[i])
		fn(&s.db.Runs[i])
		s.db.Runs[i] = detachRun(s.db.Runs[i])
		if s.db.Runs[i].Status != model.RunRunning && s.db.Runs[i].CancelCause != model.CancelCauseActionWait {
			s.markActionsUnknownLocked(id)
		}
		return detachRun(s.db.Runs[i]), s.flush("run", "action", "intent")
	}
	return model.Run{}, fmt.Errorf("run %s not found", id)
}

func (s *Store) GetRun(id string) (model.Run, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, r := range s.db.Runs {
		if r.ID == id {
			return detachRun(r), true
		}
	}
	return model.Run{}, false
}

// ProgressRun records the live context pulse of a running run without flushing
// to disk: one write per model call would make the ledger the bottleneck, and
// the settled run is written by finish (the same contract as NoteEvent).
func (s *Store) ProgressRun(id string, contextTokens int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.db.Runs {
		if s.db.Runs[i].ID != id {
			continue
		}
		if contextTokens > s.db.Runs[i].ContextTokens {
			s.db.Runs[i].ContextTokens = contextTokens
		}
		return
	}
}

// ProgressWindow records the harness-resolved context window while the run is
// live, without flushing: the window is constant for the run, so the first
// report is the useful one, and the settled run is written by finish. The
// console pressure bar needs it before the run ends (D80).
func (s *Store) ProgressWindow(id string, window int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.db.Runs {
		if s.db.Runs[i].ID != id {
			continue
		}
		s.db.Runs[i].ContextWindow = window
		return
	}
}

// RecordRunHandoffDone stores the commander pressure cycle's state at the end of
// a round. It deliberately skips the decider's change counter (flushNoRev): this
// is scheduler memory recovered after a restart, not a fact/intent graph change,
// and waking the commander for its own bookkeeping is exactly what D79 forbids.
func (s *Store) RecordRunHandoffDone(id string, done bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.db.Runs {
		if s.db.Runs[i].ID != id {
			continue
		}
		if s.db.Runs[i].HandoffDone == done {
			return nil
		}
		s.db.Runs[i].HandoffDone = done
		return s.flushNoRev("run")
	}
	return fmt.Errorf("run %s not found", id)
}

// Runs returns detached copies, one per row, for the same reason Snapshot does:
// a caller keeps the values after the lock is released while writers edit the
// live rows in place (UpdateRun, and the instruction notice/ack paths write into
// the row's maps directly). A shallow copy shares those maps with the ledger, so
// a reader that marshals a run while an executor acknowledges an instruction
// races a map write — Go turns that into `fatal error: concurrent map read and
// map write`, which no recover catches: the whole control plane dies with every
// project's scheduling. Detaching is the same rule detachIntent follows.
func (s *Store) Runs() []model.Run {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]model.Run, len(s.db.Runs))
	for i, r := range s.db.Runs {
		out[i] = detachRun(r)
	}
	return out
}

func (s *Store) Evidence() []model.Evidence {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]model.Evidence, len(s.db.Evidence))
	copy(out, s.db.Evidence)
	return out
}

// ---- goals ----

// AddGoal appends one goal. It is the plain create path the operator console
// uses (the agent surface cannot create goals); the explicit "append a goal and
// continue" path uses AddGoalIdempotent so a retried request finds the goal it
// already created instead of appending a second one.
func (s *Store) AddGoal(g model.Goal) (model.Goal, error) {
	out, _, err := s.AddGoalIdempotent(g)
	return out, err
}

func (s *Store) Goals() []model.Goal {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]model.Goal, len(s.db.Goals))
	for i, g := range s.db.Goals {
		out[i] = markGoalReceipt(detachGoal(g), s.db.Facts)
	}
	return out
}

// PinGoal marks a goal met. It refuses to do so without an evidence row that
// exists and a written reason: "盖章必须有实测" is enforced here, not in the
// prompt, and a stamp nobody can explain is not a decision the page can show.
// from is the cited fact list; it may be empty for an operator's manual pin only
// when reviews are disabled (the console then shows the evidence row instead) —
// with reviews on, every cited fact still needs a current proof.
func (s *Store) PinGoal(id, evidenceID, note string, from []string, policies ...string) (model.Goal, error) {
	return s.PinGoalByRun(id, evidenceID, note, from, "", policies...)
}

// PinGoalByRun is PinGoal with the stamp's author: the commander run that
// completed the goal, or "" for the operator's manual pin (and for every stamp
// written before MetByRun existed). ReopenGoal's caller uses this to decide
// whether a session may withdraw the stamp at all.
// GoalGate is the completion-side counterpart of DispatchGate: the one runtime
// fact that changes what verified_only means. With reviews off no verdict can
// ever arrive, so the verdict gate is dropped exactly as readiness drops it;
// withdrawn, rejected and non-reviewable premises still refuse. Without this,
// a project with reviews disabled could never stamp a goal: complete required a
// proof that the same project had switched off producing.
type GoalGate struct {
	ReviewsOff bool
}

func (s *Store) PinGoalByRun(id, evidenceID, note string, from []string, byRun string, policies ...string) (model.Goal, error) {
	return s.PinGoalByRunGate(id, evidenceID, note, from, byRun, GoalGate{}, policies...)
}

// PinGoalByRunGate is PinGoalByRun with the runtime review state. See GoalGate.
func (s *Store) PinGoalByRunGate(id, evidenceID, note string, from []string, byRun string, gate GoalGate, policies ...string) (model.Goal, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	evidenceID = strings.TrimSpace(evidenceID)
	if evidenceID == "" {
		return model.Goal{}, fmt.Errorf("盖章必须挂证据：evidence_id 不能为空")
	}
	note = strings.TrimSpace(note)
	if note == "" {
		return model.Goal{}, fmt.Errorf("盖章必须写理由：说清这些证据为什么足以证明目标达成")
	}
	from = cleanIDs(from)
	policy := model.PremiseVerifiedOnly
	if len(policies) > 0 {
		policy = policies[0]
	}
	if !model.ValidPremisePolicy(policy) {
		return model.Goal{}, fmt.Errorf("unknown premise policy %q", policy)
	}
	if policy == model.PremiseVerifiedOnly && len(from) == 0 && !gate.ReviewsOff {
		return model.Goal{}, fmt.Errorf("本模式完成目标必须引用已独立核验的事实")
	}
	claims := make(map[string]uint64, len(from))
	knownEvidence := map[string]bool{}
	for _, e := range s.db.Evidence {
		knownEvidence[e.ID] = true
	}
	associated := false
	for _, fid := range from {
		r, ok := s.records[recordKey("fact", fid)]
		if !ok {
			return model.Goal{}, fmt.Errorf("引用事实 %s 不存在", fid)
		}
		f := r.value.(model.Fact)
		if f.Unusable() || len(f.EvidenceIDs) == 0 {
			return model.Goal{}, fmt.Errorf("引用事实 %s 已撤回/已否定或没有证据，不能支持目标达成", fid)
		}
		available := false
		for _, eid := range f.EvidenceIDs {
			if knownEvidence[eid] {
				available = true
			}
			if eid == evidenceID {
				associated = true
			}
		}
		if !available {
			return model.Goal{}, fmt.Errorf("事实 %s 引用的证据均不存在", fid)
		}
		if policy == model.PremiseVerifiedOnly {
			if issue := model.PremiseIssue(f, policy); issue != "" {
				// A reviewable, merely-unverified row is waiting on a verdict. With
				// reviews off that verdict can never arrive, so the gate is the only
				// thing standing between the fact and admission — drop it, like
				// readiness does. Withdrawn/rejected/non-reviewable stays refused.
				if !(gate.ReviewsOff && model.PremiseIssueReviewOnly(f)) {
					return model.Goal{}, fmt.Errorf("事实 %s：%s", fid, issue)
				}
			}
			if _, ok := model.CurrentProof(f, s.db.Reviews); !ok && !gate.ReviewsOff {
				return model.Goal{}, fmt.Errorf("事实 %s 缺少当前断言的独立核验收据", fid)
			}
		}
		claims[fid] = f.ClaimVersion
	}
	if len(from) > 0 && !associated {
		return model.Goal{}, fmt.Errorf("所选证据不属于引用事实，不能写入完成收据")
	}
	found := false
	for _, e := range s.db.Evidence {
		if e.ID == evidenceID {
			found = true
			break
		}
	}
	if !found {
		return model.Goal{}, fmt.Errorf("evidence %s 不存在：盖章必须挂真实证据", evidenceID)
	}
	for i := range s.db.Goals {
		if s.db.Goals[i].ID != id {
			continue
		}
		if s.db.Goals[i].Met {
			return model.Goal{}, fmt.Errorf("目标 %s 已经达成", id)
		}
		if s.db.Goals[i].Closed {
			return model.Goal{}, fmt.Errorf("目标 %s 已关闭", id)
		}
		now := time.Now()
		s.db.Goals[i].Met = true
		s.db.Goals[i].EvidenceID = evidenceID
		s.db.Goals[i].Note = note
		s.db.Goals[i].From = from
		s.db.Goals[i].FromClaimVersions = claims
		s.db.Goals[i].CompletedAt = &now
		s.db.Goals[i].CompletionPolicy = policy
		s.db.Goals[i].MetByRun = strings.TrimSpace(byRun)
		return markGoalReceipt(detachGoal(s.db.Goals[i]), s.db.Facts), s.flush("goal")
	}
	return model.Goal{}, fmt.Errorf("goal %s not found", id)
}

// ReopenGoal withdraws a stamp. Since D97 the judgement belongs to the
// commander, and a judgement can be wrong; until this existed there was no way
// back — the only workaround was to add another goal, which hid the mistake
// instead of correcting it (D98). The receipt is cleared with the stamp:
// note/from/completed_at describe a decision that no longer stands, and a
// reason sitting next to "未达成" would read as if the goal were still met. The
// round that made the stamp still names it in that round's decisions.jsonl.
//
// Only a met goal can be reopened: an open goal has no stamp to withdraw.
func (s *Store) ReopenGoal(id string) (model.Goal, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.db.Goals {
		if s.db.Goals[i].ID != id {
			continue
		}
		if !s.db.Goals[i].Met {
			return model.Goal{}, fmt.Errorf("目标 %s 没有盖章，无需撤销", id)
		}
		if s.db.Goals[i].Closed {
			return model.Goal{}, fmt.Errorf("目标 %s 已关闭", id)
		}
		s.db.Goals[i].Met = false
		s.db.Goals[i].EvidenceID = ""
		s.db.Goals[i].Note = ""
		s.db.Goals[i].From = nil
		s.db.Goals[i].FromClaimVersions = nil
		s.db.Goals[i].CompletedAt = nil
		s.db.Goals[i].CompletionPolicy = ""
		s.db.Goals[i].MetByRun = ""
		s.db.Goals[i].ReceiptStale, s.db.Goals[i].ReceiptChanged = false, nil
		return detachGoal(s.db.Goals[i]), s.flush("goal")
	}
	return model.Goal{}, fmt.Errorf("goal %s not found", id)
}

// cleanIDs trims, drops empties and de-duplicates while keeping the caller's
// order: the citation list is a receipt, so it must not silently reorder.
func cleanIDs(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	seen := make(map[string]bool, len(in))
	for _, v := range in {
		v = strings.TrimSpace(v)
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}

// ---- reviews ----

// ReviewFact records one verifier verdict and moves the fact's status with it.
// A verdict nobody can reproduce must not leave the fact looking proven, so
// "needs_evidence" keeps it unverified rather than leaving the old status.
//
// A non-zero ClaimVersion binds the verdict to the assertion the verifier
// actually proved: an asynchronous verifier must carry the version it started
// with, and a fact changed meanwhile rejects the stale verdict
// (ErrReviewConflict) instead of letting an old proof speak for new content. A
// zero version is the synchronous human verdict: it is recorded under this
// lock, so "the assertion I judged" is exactly the row being changed.
func (s *Store) ReviewFact(r model.Review) (model.Review, error) {
	s.mu.Lock()

	var status string
	switch r.Verdict {
	case "pass":
		status = "verified"
	case "reject":
		status = "rejected"
	case "needs_evidence":
		status = "unverified"
	default:
		s.mu.Unlock()
		return model.Review{}, fmt.Errorf("verdict 不合法：%q", r.Verdict)
	}

	idx := -1
	for i := range s.db.Facts {
		if s.db.Facts[i].ID == r.FactID {
			idx = i
			break
		}
	}
	if idx < 0 {
		s.mu.Unlock()
		return model.Review{}, fmt.Errorf("fact %s not found", r.FactID)
	}
	fact := &s.db.Facts[idx]
	if fact.Status == "withdrawn" {
		s.mu.Unlock()
		return model.Review{}, fmt.Errorf("事实已撤回，不能复核；需要重新采用请先更正断言或由操作员重新裁定")
	}
	if r.ClaimVersion != 0 && (r.ClaimVersion != fact.ClaimVersion || r.ClaimHash != "" && r.ClaimHash != fact.ClaimHash) {
		s.mu.Unlock()
		return model.Review{}, fmt.Errorf("%w（判定 v%d，当前 v%d）", ErrReviewConflict, r.ClaimVersion, fact.ClaimVersion)
	}
	// The verdict is the store's alone, so the evidence condition lives here and
	// not in each caller: a pass on a finding with nothing preserved would make
	// the claim "verified" on the strength of a sentence, and the report reads
	// that field as proof. Refused before the label is touched, with the reason
	// the operator can act on.
	if r.Verdict == "pass" {
		if reason := s.reviewPassBlockedLocked(*fact); reason != "" {
			s.mu.Unlock()
			return model.Review{}, errors.New(reason)
		}
	}
	// A pass proves the assertion, not the chain above it. When the direction's
	// premises are not verified the verdict still stands — refusing it would
	// deadlock every downstream row behind an upstream nobody re-reviews — but the
	// claim may not become "observed" on the strength of it, and the review says
	// which premise is missing so the operator can see the gap. The label is
	// derived, so a verdict never rewrites the claim version.
	s.deriveConfidenceLocked(fact)
	if r.Verdict == "pass" {
		if bad, unverified := s.upstreamUnverifiedLocked(*fact); unverified {
			if r.Notes == "" {
				r.Notes = "premise_unverified:" + bad
			} else {
				r.Notes += "；premise_unverified:" + bad
			}
		}
	}
	r.ClaimVersion, r.ClaimHash = fact.ClaimVersion, fact.ClaimHash
	r.ID = s.uniqueIDLocked("rv")
	if r.CreatedAt.IsZero() {
		r.CreatedAt = time.Now()
	}
	fact.Status = status
	s.db.Reviews = append(s.db.Reviews, r)
	// Anything short of a pass withdraws the backing this row gave the rows
	// below. A rejection says the claim does not reproduce; needs_evidence says
	// the proof was never substantiated. Neither proves a downstream conclusion
	// false, so those rows lose the endorsement (status back to unverified) and
	// are never rejected. Every verdict also re-reads their label: a pass lets
	// them fall back to their own declared base, everything else keeps them at
	// inferred.
	lost := s.refreshDownstreamLocked(r.FactID, r.Verdict != "pass")
	reason := ""
	if len(lost) > 0 {
		reason = premiseLostReason + r.FactID
	}
	err := s.flushFactChangesLocked(reason, lost, "fact", "review")
	hook := s.onFact
	f := *fact
	uris := s.evidenceURIsLocked(f.EvidenceIDs)
	s.mu.Unlock()

	// The mirror carries the verdict: an unverified finding that passes review
	// must stop looking provisional in the library.
	if err == nil && hook != nil {
		hook(f, uris)
	}
	if err == nil {
		s.notifyFactChange()
	}
	return r, err
}

func (s *Store) Reviews(factID string) []model.Review {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]model.Review, 0, len(s.db.Reviews))
	for _, r := range s.db.Reviews {
		if factID == "" || r.FactID == factID {
			out = append(out, r)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}

// ---- playbooks ----

// playbookKey is the duplicate guard: case and surrounding whitespace only.
// Folding punctuation away made different tactics collide, the same mistake the
// intent title guard avoids (D21, D54).
func playbookKey(title string) string {
	return strings.ToLower(strings.TrimSpace(title))
}

func (s *Store) playbookIndexLocked(title string) int {
	key := playbookKey(title)
	if key == "" {
		return -1
	}
	for i := range s.db.Playbooks {
		if playbookKey(s.db.Playbooks[i].Title) == key {
			return i
		}
	}
	return -1
}

// Playbooks returns the whole table, most recently touched first. It is a copy:
// callers sort and filter their own view.
func (s *Store) Playbooks() []model.Playbook {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]model.Playbook, len(s.db.Playbooks))
	copy(out, s.db.Playbooks)
	for i := range out {
		out[i] = detachPlaybook(out[i])
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	return out
}

func (s *Store) PlaybookByID(id string) (model.Playbook, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, p := range s.db.Playbooks {
		if p.ID == id {
			return detachPlaybook(p), true
		}
	}
	return model.Playbook{}, false
}

// UpsertPlaybook writes one entry, deduplicated by title. It returns the stored
// row and whether the write changed anything (false means a confirmed or
// retired row already existed and was left alone).
//
// The two writers are not equal. An executor declaration is a live observation
// of the tactic and always wins: it overwrites content, promotes a candidate,
// revives a retired row and confirms the entry. A commander nomination may only
// create or refine a candidate; it can never rewrite a row an executor
// confirmed, because the commander never saw the bytes (D70).
func (s *Store) UpsertPlaybook(p model.Playbook) (model.Playbook, bool, error) {
	p.Title = strings.TrimSpace(p.Title)
	p.When = strings.TrimSpace(p.When)
	p.Doc = strings.TrimSpace(p.Doc)
	p.Tags = cleanTags(p.Tags)
	p.RequiredTags = cleanRequirements(p.RequiredTags)
	if err := ValidatePattern(p.Pattern); err != nil {
		return model.Playbook{}, false, err
	}
	p = detachPlaybook(p)
	if p.Title == "" {
		return model.Playbook{}, false, fmt.Errorf("playbook 需要 title")
	}
	if p.Outcome != model.PlaybookDeadEnd {
		p.Outcome = model.PlaybookSuccess
	}
	if p.Origin != model.PlaybookFromExecutor {
		p.Origin = model.PlaybookFromCommander
	}
	// Status is the write-time declaration only: an executor's declaration is
	// recorded as confirmed because the writer used the tactic in this run. It is
	// not the answer to "may this be called confirmed" — that is
	// model.Playbook.Confirmed, re-derived on every read from the source facts.
	// The writer cannot decide it here: the source facts are admitted unverified
	// and the review runs later, so a source check at write time would freeze
	// every legitimate tactic as a candidate with no way to promote it.
	if p.Status == "" {
		if p.Origin == model.PlaybookFromExecutor {
			p.Status = model.PlaybookConfirmed
		} else {
			p.Status = model.PlaybookCandidate
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	idx := s.playbookIndexLocked(p.Title)
	if idx < 0 {
		p.FromFactVersions = map[string]uint64{}
		for _, id := range p.FromFactIDs {
			if r, ok := s.records[recordKey("fact", id)]; ok {
				p.FromFactVersions[id] = r.version
			}
		}
		p.ID = s.uniqueIDLocked("pb")
		p.CreatedAt, p.UpdatedAt = now, now
		s.db.Playbooks = append(s.db.Playbooks, p)
		return detachPlaybook(p), true, s.flush("playbook")
	}

	cur := &s.db.Playbooks[idx]
	p.ID = cur.ID
	switch {
	case p.Origin == model.PlaybookFromExecutor:
		// Same write-time declaration as the new-row branch above: the executor
		// that used the tactic wins and the row is recorded as confirmed. Whether
		// the sources support that label is decided on read by
		// model.Playbook.Confirmed; FromFactVersions is provenance, not a gate.
		if p.Pattern != nil {
			cur.Pattern = p.Pattern
		}
		cur.SourceRunID = p.SourceRunID
		if p.RequiredTags != nil {
			cur.RequiredTags = append([]string(nil), p.RequiredTags...)
		}
		if p.When != "" {
			cur.When = p.When
		}
		if p.Doc != "" {
			cur.Doc = p.Doc
		}
		cur.Outcome = p.Outcome
		cur.Status = model.PlaybookConfirmed
		cur.Origin = model.PlaybookFromExecutor
		cur.IntentID = p.IntentID
		cur.Tags = mergeTags(cur.Tags, p.Tags)
		cur.FromFactIDs = mergeIDs(cur.FromFactIDs, p.FromFactIDs)
		cur.UpdatedAt = now
	case cur.Status == model.PlaybookCandidate:
		if p.Pattern != nil {
			cur.Pattern = p.Pattern
		}
		if p.RequiredTags != nil {
			cur.RequiredTags = append([]string(nil), p.RequiredTags...)
		}
		// The latest nomination wins: candidates are proposals, not decisions.
		if p.When != "" {
			cur.When = p.When
		}
		if p.Doc != "" {
			cur.Doc = p.Doc
		}
		cur.Outcome = p.Outcome
		cur.Tags = mergeTags(cur.Tags, p.Tags)
		cur.FromFactIDs = mergeIDs(cur.FromFactIDs, p.FromFactIDs)
		cur.UpdatedAt = now
	default:
		// A confirmed or retired entry is not the commander's to edit.
		return detachPlaybook(*cur), false, nil
	}
	if cur.FromFactVersions == nil {
		cur.FromFactVersions = map[string]uint64{}
	}
	for _, id := range p.FromFactIDs {
		if r, ok := s.records[recordKey("fact", id)]; ok {
			cur.FromFactVersions[id] = r.version
		}
	}
	return detachPlaybook(*cur), true, s.flush("playbook")
}

// RetirePlaybook drops an entry out of recall without deleting its text. A
// wrong tactic is still a lesson: keeping it retired is friendlier than making
// the same mistake again to relearn it.
func (s *Store) RetirePlaybook(id string) (model.Playbook, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.db.Playbooks {
		if s.db.Playbooks[i].ID != id {
			continue
		}
		s.db.Playbooks[i].Status = model.PlaybookRetired
		s.db.Playbooks[i].UpdatedAt = time.Now()
		return detachPlaybook(s.db.Playbooks[i]), s.flush("playbook")
	}
	return model.Playbook{}, fmt.Errorf("playbook %s not found", id)
}

var ErrPlaybookChanged = errors.New("战场知识已更新，请刷新后再操作")

// DeletePlaybook is explicit operator curation. It never removes source facts
// or independently published world knowledge, and does not wake a model.
func (s *Store) DeletePlaybook(id string, updatedAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, p := range s.db.Playbooks {
		if p.ID != id {
			continue
		}
		if !p.UpdatedAt.Equal(updatedAt) {
			return ErrPlaybookChanged
		}
		s.db.Playbooks = append(s.db.Playbooks[:i], s.db.Playbooks[i+1:]...)
		return s.flushNoRev("playbook")
	}
	return os.ErrNotExist
}

func cleanTags(in []string) []string {
	out := make([]string, 0, len(in))
	for _, t := range in {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		out = append(out, t)
		if len(out) >= 8 {
			break
		}
	}
	return out
}

func mergeTags(a, b []string) []string {
	seen := make(map[string]bool, len(a)+len(b))
	out := make([]string, 0, len(a)+len(b))
	for _, list := range [][]string{a, b} {
		for _, t := range list {
			t = strings.TrimSpace(t)
			if t == "" || seen[strings.ToLower(t)] {
				continue
			}
			seen[strings.ToLower(t)] = true
			out = append(out, t)
			if len(out) >= 8 {
				return out
			}
		}
	}
	return out
}

func mergeIDs(a, b []string) []string {
	seen := make(map[string]bool, len(a)+len(b))
	out := make([]string, 0, len(a)+len(b))
	for _, list := range [][]string{a, b} {
		for _, id := range list {
			id = strings.TrimSpace(id)
			if id == "" || seen[id] {
				continue
			}
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

// ---- point reads ----

func (s *Store) FactByID(id string) (model.Fact, bool) {
	f, _, ok := s.FactVersion(id)
	return f, ok
}

func (s *Store) FactVersion(id string) (model.Fact, uint64, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.records[recordKey("fact", id)]
	if !ok {
		return model.Fact{}, 0, false
	}
	f, ok := r.value.(model.Fact)
	return detachFact(f), r.version, ok
}

func (s *Store) EvidenceByID(id string) (model.Evidence, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, e := range s.db.Evidence {
		if e.ID == id {
			return e, true
		}
	}
	return model.Evidence{}, false
}

// SearchFacts is the "depth by query" path: the board never carries the full
// text, so looking something up is an explicit act with an explicit limit.
func (s *Store) SearchFacts(q, kind string, limit int) []model.Fact {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	q = strings.ToLower(strings.TrimSpace(q))
	out := make([]model.Fact, 0, limit)
	for _, f := range s.db.Facts {
		if kind != "" && f.Kind != kind {
			continue
		}
		if q != "" &&
			!strings.Contains(strings.ToLower(f.Title), q) &&
			!strings.Contains(strings.ToLower(f.Summary), q) &&
			!strings.Contains(strings.ToLower(f.Doc), q) {
			continue
		}
		out = append(out, f)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// Snapshot returns a detached copy of the ledger. It must be a copy, not a
// slice of the live DB: writers mutate rows in place under the lock
// (UpdateFact, UpdateIntent, UpdateRun, RetirePlaybook), so handing the live
// slices to a caller that reads them without the lock is a data race and a
// torn read — and every caller here (board, prompt, report) does exactly that.
func (s *Store) Snapshot() DB {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := s.db.clone()
	out.ReadRevision = s.rev
	if s.db.Memory != nil {
		out.ContentHash = s.db.Memory.ContentHash
	} else {
		out.ContentHash = contentHash(s.records)
	}
	return out
}

// ProjectSessionFacts caps a frozen read at the current visibility of each
// assertion. A cursor can retain ordering, never access to a revoked claim.
func (s *Store) ProjectSessionFacts(facts []model.Fact) {
	s.projectSessionFacts(facts, false)
}

// ProjectSessionFactsVisible is ProjectSessionFacts with review off: an
// unverified claim may keep its text (there is no verdict to wait for), but the
// redaction of withdrawn, rejected, missing or re-stated rows still applies — a
// stale or retracted claim must never become readable because a switch was
// flipped.
func (s *Store) ProjectSessionFactsVisible(facts []model.Fact) {
	s.projectSessionFacts(facts, true)
}

func (s *Store) projectSessionFacts(facts []model.Fact, showUnverified bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for i, f := range facts {
		current, ok := s.records[recordKey("fact", f.ID)]
		if ok {
			live := current.value.(model.Fact)
			if f.Status == "verified" && live.Status == "verified" &&
				f.ClaimVersion == live.ClaimVersion && f.ClaimHash == live.ClaimHash {
				continue
			}
			if showUnverified && live.Status == "unverified" &&
				f.ClaimVersion == live.ClaimVersion && f.ClaimHash == live.ClaimHash {
				facts[i] = live
				continue
			}
			f.Status = live.Status
		} else {
			f.Status = "missing"
		}
		facts[i] = model.FactPlaceholder(f)
	}
}

// clone copies the top-level slices. Row values never share mutable state with
// the live DB: writers replace rows wholesale (a new Tags/EvidenceIDs slice, a
// new *time.Time), they do not edit the slices inside a row.
func (db DB) clone() DB {
	out := db
	out.Notebooks = append([]model.Notebook(nil), db.Notebooks...)
	for i := range out.Notebooks {
		out.Notebooks[i] = detachNotebook(out.Notebooks[i])
	}
	out.Routes = append([]model.RouteCheckpoint(nil), db.Routes...)
	out.Instructions = append([]model.Instruction(nil), db.Instructions...)
	out.Actions = append([]model.ExternalAction(nil), db.Actions...)
	out.Intents = append([]model.Intent(nil), db.Intents...)
	for i := range out.Intents {
		out.Intents[i] = detachIntent(out.Intents[i])
	}
	out.Facts = append([]model.Fact(nil), db.Facts...)
	for i := range out.Facts {
		out.Facts[i] = detachFact(out.Facts[i])
	}
	out.Evidence = append([]model.Evidence(nil), db.Evidence...)
	out.Goals = append([]model.Goal(nil), db.Goals...)
	for i := range out.Goals {
		// The snapshot is read by the board and the graph: they need the same
		// "the cited claim has moved" signal the goals API carries, and it is
		// computed on the copy so the stored stamp is never rewritten.
		out.Goals[i] = markGoalReceipt(detachGoal(out.Goals[i]), out.Facts)
	}
	out.Reviews = append([]model.Review(nil), db.Reviews...)
	out.Runs = append([]model.Run(nil), db.Runs...)
	for i := range out.Runs {
		out.Runs[i] = detachRun(out.Runs[i])
	}
	out.Playbooks = append([]model.Playbook(nil), db.Playbooks...)
	for i := range out.Playbooks {
		out.Playbooks[i] = detachPlaybook(out.Playbooks[i])
	}
	out.OperatorRequests = append([]model.OperatorRequest(nil), db.OperatorRequests...)
	out.Messages = append([]model.Message(nil), db.Messages...)
	out.Documents = append([]model.Document(nil), db.Documents...)
	return out
}

// Path is where the ledger file lives. The bundle layer needs it to carry the
// ledger inside the archive.
func (s *Store) Path() string { return s.path }

// Revision is a monotonic count of durable writes to this ledger. It is not
// persisted: a restart starts at zero and the first board after it therefore
// looks changed, which is exactly right — no round has seen the reloaded world.
// The board puts it in the decider digest (D79); everything else ignores it.
func (s *Store) Revision() uint64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.rev
}
