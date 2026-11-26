// Package collab issues and resolves the short-lived identity a running
// executor uses to talk to its peers and read the live project graph.
//
// The console token is an instance credential: every local process that
// inherits the host environment could read it, and it does not say which run is
// calling. A participant token is the opposite: one run, one intent, one
// generation, one project, revoked when the run ends. The token never comes
// from the request body; the registry resolves it to the caller, so a run
// cannot claim to be another participant.
package collab

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sync"
	"time"
)

// Identity is the server-side truth about one running executor. Token is only
// returned to the caller that issues it (it goes into the run's case file); the
// API's participant views omit it.
type Identity struct {
	Token         string    `json:"token"`
	ProjectID     string    `json:"project_id"`
	ParticipantID string    `json:"participant_id"`
	IntentID      string    `json:"intent_id"`
	RunID         string    `json:"run_id"`
	Generation    int       `json:"generation"`
	APIBase       string    `json:"api_base"`
	ExpiresAt     time.Time `json:"expires_at"`
}

// CommanderID is the project commander's participant id. It is stable and
// reserved: an intent cannot mint it because participant ids are derived from
// intent ids.
const CommanderID = "commander"

// OperatorID is the person at the console. Incoming messages are durable
// console records; there is no model session to wake. Acceptance is not read.
const OperatorID = "operator"

// ParticipantID is the stable collaboration identity of an intent. It survives
// run generations: the same direction keeps one address, while the token's
// generation says which holder is currently speaking.
func ParticipantID(intentID string) string { return "pt_" + intentID }

// IntentIDFromParticipant reverses ParticipantID. ok=false means the id is not
// an intent participant (for example the commander).
func IntentIDFromParticipant(participant string) (string, bool) {
	const prefix = "pt_"
	if len(participant) <= len(prefix) || participant[:len(prefix)] != prefix {
		return "", false
	}
	return participant[len(prefix):], true
}

// ReadOnlyCredential is the credential of a support run that may read one
// project's battlefield records and nothing else. A review is the case it exists
// for: it must open the evidence a fact cites, and it must not become a second
// direction — no speaking as a participant, no writes, no list-wide scouting.
// It is a separate type from Identity on purpose: Resolve never returns one, so
// a read-only token cannot be spent as a session credential by mistake.
type ReadOnlyCredential struct {
	ProjectID string
	RunID     string
	ExpiresAt time.Time
}

// Registry holds the live tokens of one process. It is deliberately in-memory:
// a token belongs to a live run, and a restart stops that run too. A token that
// survived a restart would be a credential nobody can reason about any more.
type Registry struct {
	ttl time.Duration

	mu     sync.RWMutex
	byHash map[[32]byte]Identity
	byRun  map[string][][32]byte
	// readByHash is the read-only side: same lifecycle, narrower reach.
	readByHash map[[32]byte]ReadOnlyCredential
	readByRun  map[string][][32]byte
}

func NewRegistry(ttl time.Duration) *Registry {
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	return &Registry{
		ttl:        ttl,
		byHash:     map[[32]byte]Identity{},
		byRun:      map[string][][32]byte{},
		readByHash: map[[32]byte]ReadOnlyCredential{},
		readByRun:  map[string][][32]byte{},
	}
}

// IssueReadOnly mints a credential bound to one project and one run. The run is
// part of the binding so RevokeRun ends it with the run that was given it; the
// gate decides what it may reach (cmd/saker/auth.go verifierReadAllowed), this
// registry only says which project and whether it is still alive.
func (r *Registry) IssueReadOnly(projectID, runID string) (string, error) {
	if r == nil {
		return "", errors.New("协作身份未启用")
	}
	if projectID == "" || runID == "" {
		return "", errors.New("只读凭证缺少 project/run")
	}
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	token := hex.EncodeToString(raw[:])
	key := sha256.Sum256([]byte(token))
	r.mu.Lock()
	r.readByHash[key] = ReadOnlyCredential{ProjectID: projectID, RunID: runID, ExpiresAt: time.Now().Add(r.ttl)}
	r.readByRun[runID] = append(r.readByRun[runID], key)
	r.mu.Unlock()
	return token, nil
}

// ResolveReadOnly maps a bearer to the project it may read. Expired entries are
// dropped on the way out, like Resolve's.
func (r *Registry) ResolveReadOnly(token string) (ReadOnlyCredential, bool) {
	if r == nil || token == "" {
		return ReadOnlyCredential{}, false
	}
	key := sha256.Sum256([]byte(token))
	r.mu.RLock()
	cred, ok := r.readByHash[key]
	r.mu.RUnlock()
	if !ok {
		return ReadOnlyCredential{}, false
	}
	if !cred.ExpiresAt.IsZero() && time.Now().After(cred.ExpiresAt) {
		r.mu.Lock()
		delete(r.readByHash, key)
		r.mu.Unlock()
		return ReadOnlyCredential{}, false
	}
	return cred, true
}

// Issue creates a token for one run. The caller fills ProjectID, ParticipantID,
// IntentID, RunID and Generation; Token and ExpiresAt are assigned here.
//
// The commander is the one identity without an intent: its token is the
// per-session credential for the commander note (state.md), not an address other
// participants can message. Everyone else must name the intent they act for.
func (r *Registry) Issue(in Identity) (Identity, error) {
	if r == nil {
		return Identity{}, errors.New("协作身份未启用")
	}
	if in.ProjectID == "" || in.ParticipantID == "" || in.RunID == "" {
		return Identity{}, errors.New("协作身份缺少 project/participant/run")
	}
	if in.IntentID == "" && in.ParticipantID != CommanderID {
		return Identity{}, errors.New("协作身份缺少 intent")
	}
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return Identity{}, err
	}
	in.Token = hex.EncodeToString(raw[:])
	in.ExpiresAt = time.Now().Add(r.ttl)
	key := sha256.Sum256([]byte(in.Token))
	r.mu.Lock()
	r.byHash[key] = in
	r.byRun[in.RunID] = append(r.byRun[in.RunID], key)
	r.mu.Unlock()
	return in, nil
}

// Resolve maps a bearer token to its identity. Expired entries are dropped on
// the way out, so a long-lived process does not accumulate dead tokens.
func (r *Registry) Resolve(token string) (Identity, bool) {
	if r == nil || token == "" {
		return Identity{}, false
	}
	key := sha256.Sum256([]byte(token))
	r.mu.RLock()
	in, ok := r.byHash[key]
	r.mu.RUnlock()
	if !ok {
		return Identity{}, false
	}
	if !in.ExpiresAt.IsZero() && time.Now().After(in.ExpiresAt) {
		r.revokeHash(key)
		return Identity{}, false
	}
	return in, true
}

// Adopt points an already-issued token at the run now using it. The commander's
// credential is per session, not per run: rotating the token every round would
// invalidate the $SAKER_AUTH a long-lived shell exported once. Its identity must
// still follow the live round, or participant messages and audit events would be
// attributed to the finished run that first received the token. The token hash
// is unchanged, so the shell's value keeps working; only the bookkeeping moves.
func (r *Registry) Adopt(token, runID string, generation int) bool {
	if r == nil || token == "" || runID == "" {
		return false
	}
	key := sha256.Sum256([]byte(token))
	r.mu.Lock()
	defer r.mu.Unlock()
	in, ok := r.byHash[key]
	if !ok {
		return false
	}
	old := in.RunID
	in.RunID, in.Generation = runID, generation
	r.byHash[key] = in
	if old == runID {
		return true
	}
	if keys := r.byRun[old]; len(keys) > 0 {
		kept := keys[:0]
		for _, k := range keys {
			if k != key {
				kept = append(kept, k)
			}
		}
		if len(kept) == 0 {
			delete(r.byRun, old)
		} else {
			r.byRun[old] = kept
		}
	}
	r.byRun[runID] = append(r.byRun[runID], key)
	return true
}

// RevokeRun ends every token of one run. It is called when the run settles, so
// a stale executor cannot keep talking after its turn is over. Read-only
// credentials carry the run they were issued for, so a review's credential dies
// here too — a finished review has nothing left to look up.
func (r *Registry) RevokeRun(runID string) {
	if r == nil || runID == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	keys := append([][32]byte(nil), r.byRun[runID]...)
	delete(r.byRun, runID)
	for _, key := range keys {
		delete(r.byHash, key)
	}
	readKeys := append([][32]byte(nil), r.readByRun[runID]...)
	delete(r.readByRun, runID)
	for _, key := range readKeys {
		delete(r.readByHash, key)
	}
}

func (r *Registry) revokeHash(key [32]byte) {
	r.mu.Lock()
	delete(r.byHash, key)
	r.mu.Unlock()
}
