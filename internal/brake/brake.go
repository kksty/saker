// Package brake bounds how often the control plane repeats one recovery action
// on one shared dependency.
//
// Per-direction budgets are necessary and not sufficient. The failure they miss
// is the shared one: when a model gateway, a credential or a scan node is down,
// every direction fails for the same reason, and each of them may spend its own
// retry budget without ever breaking a rule. Nobody exceeds a limit and the
// fleet still hammers a link that is already down — the incident where every
// key in the pool was dead and the retry loop kept relaunching the fleet.
//
// So the unit is the resource, not the work: the caller names the dependency it
// is about to retry (gateway, node, key) and asks for a slot. Slots are spaced
// by Cooldown and capped per Window. A refused reservation is not an error and
// not a decision about the work: the caller leaves the direction for its own
// supervisor, exactly as it would after exhausting a per-direction budget.
//
// The brake does not know what a "correct" rate is for a given dependency — it
// only makes the rate a number somebody chose, instead of the product of how
// many directions happen to exist.
package brake

import (
	"sync"
	"time"
)

// Options configure one brake. Zero values fall back to conservative defaults:
// one action every 30s per key, at most 12 per 10 minutes.
type Options struct {
	Cooldown time.Duration // minimum spacing between two actions on one key
	Window   time.Duration // rolling window the cap is counted over
	Max      int           // actions allowed per window on one key
	Now      func() time.Time
}

func (o Options) withDefaults() Options {
	if o.Cooldown <= 0 {
		o.Cooldown = 30 * time.Second
	}
	if o.Window <= 0 {
		o.Window = 10 * time.Minute
	}
	if o.Max <= 0 {
		o.Max = 12
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return o
}

// Reason says why a reservation was refused.
type Reason string

const (
	// ReasonWindowCap means this key has already used its whole window. More
	// cooldown would not help; the dependency needs attention, not retries.
	ReasonWindowCap Reason = "window_cap"
)

// Slot is one granted reservation. At is the earliest instant the action may
// run. Release is for the caller that reserved and then did not act (it lost a
// race, or found the work already running): an unused reservation must not
// consume the shared budget, or the gate would punish concurrency rather than
// storms.
type Slot struct {
	brake    *Brake
	key      string
	at       time.Time
	released bool
}

// At is the instant the reservation allows.
func (s *Slot) At() time.Time { return s.at }

// Release returns the reservation to the brake. Safe to call twice.
func (s *Slot) Release() {
	if s == nil || s.released {
		return
	}
	s.released = true
	s.brake.release(s.key, s.at)
}

// Brake is safe for concurrent use.
type Brake struct {
	mu   sync.Mutex
	opts Options
	keys map[string]*history
}

type history struct {
	last       time.Time   // last granted instant on this key
	reservedAt []time.Time // granted instants still inside the window
}

// New returns a brake with the given options.
func New(opts Options) *Brake {
	return &Brake{opts: opts.withDefaults(), keys: map[string]*history{}}
}

// Reserve asks for the next free slot on key. A nil slot means the window cap
// was reached; the caller must stop retrying that dependency and leave the
// decision to its supervisor. Nothing is recorded when the request is refused.
func (b *Brake) Reserve(key string) (*Slot, Reason) {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.opts.Now()
	h := b.keys[key]
	if h == nil {
		h = &history{}
		b.keys[key] = h
	}
	b.pruneLocked(h, now)
	if len(h.reservedAt) >= b.opts.Max {
		return nil, ReasonWindowCap
	}
	at := now
	if !h.last.IsZero() {
		if next := h.last.Add(b.opts.Cooldown); next.After(at) {
			at = next
		}
	}
	h.last = at
	h.reservedAt = append(h.reservedAt, at)
	return &Slot{brake: b, key: key, at: at}, ""
}

// Reset forgets a key entirely. Call it when the dependency demonstrably works
// again (a run on it completed): the next failure is a new episode, not this one
// continuing, and it should not inherit a cooldown or a spent window.
func (b *Brake) Reset(key string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.keys, key)
}

func (b *Brake) release(key string, at time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	h := b.keys[key]
	if h == nil {
		return
	}
	for i := len(h.reservedAt) - 1; i >= 0; i-- {
		if h.reservedAt[i].Equal(at) {
			h.reservedAt = append(h.reservedAt[:i], h.reservedAt[i+1:]...)
			if h.last.Equal(at) {
				h.last = time.Time{}
				if len(h.reservedAt) > 0 {
					h.last = h.reservedAt[len(h.reservedAt)-1]
				}
			}
			return
		}
	}
}

func (b *Brake) pruneLocked(h *history, now time.Time) {
	cutoff := now.Add(-b.opts.Window)
	kept := h.reservedAt[:0]
	for _, at := range h.reservedAt {
		if at.After(cutoff) {
			kept = append(kept, at)
		}
	}
	h.reservedAt = kept
}
