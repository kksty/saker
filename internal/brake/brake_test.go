package brake

// The gate has to be conservative in two directions at once: it must actually
// bound a storm, and it must not punish concurrency. These tests pin both, plus
// the two ways a caller can be wrong about a slot it never used.

import (
	"sync"
	"testing"
	"time"
)

type clock struct{ now time.Time }

func (c *clock) Now() time.Time          { return c.now }
func (c *clock) Advance(d time.Duration) { c.now = c.now.Add(d) }

func newTestBrake(max int) (*Brake, *clock) {
	c := &clock{now: time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)}
	return New(Options{Cooldown: 30 * time.Second, Window: 10 * time.Minute, Max: max, Now: c.Now}), c
}

func TestFirstReservationIsImmediateAndTheNextIsSpaced(t *testing.T) {
	b, c := newTestBrake(12)
	first, reason := b.Reserve("gw=openai/node=local")
	if first == nil {
		t.Fatalf("first reservation refused: %s", reason)
	}
	if !first.At().Equal(c.now) {
		t.Fatalf("first slot = %v, want now (%v)", first.At(), c.now)
	}
	second, reason := b.Reserve("gw=openai/node=local")
	if second == nil {
		t.Fatalf("second reservation refused inside the cap: %s", reason)
	}
	if got, want := second.At(), c.now.Add(30*time.Second); !got.Equal(want) {
		t.Fatalf("second slot = %v, want %v (cooldown)", got, want)
	}
	// A different dependency starts on its own clock.
	other, _ := b.Reserve("gw=deepseek/node=local")
	if !other.At().Equal(c.now) {
		t.Fatalf("other key inherited a cooldown: %v", other.At())
	}
}

func TestWindowCapRefusesWithoutRecording(t *testing.T) {
	b, c := newTestBrake(2)
	if slot, _ := b.Reserve("node=scan-1"); slot == nil {
		t.Fatal("first refused")
	}
	if slot, _ := b.Reserve("node=scan-1"); slot == nil {
		t.Fatal("second refused")
	}
	if slot, reason := b.Reserve("node=scan-1"); slot != nil || reason != ReasonWindowCap {
		t.Fatalf("third = slot %v reason %q, want refusal by window cap", slot, reason)
	}
	// Past the window the key works again.
	c.Advance(10*time.Minute + time.Second)
	if slot, reason := b.Reserve("node=scan-1"); slot == nil {
		t.Fatalf("after the window: %s", reason)
	}
}

func TestReleasedSlotDoesNotConsumeTheBudget(t *testing.T) {
	b, _ := newTestBrake(1)
	only, _ := b.Reserve("gw=openai")
	if only == nil {
		t.Fatal("first refused")
	}
	if slot, reason := b.Reserve("gw=openai"); slot != nil || reason != ReasonWindowCap {
		t.Fatalf("cap not enforced: slot %v reason %q", slot, reason)
	}
	// The caller lost a race and never acted: the shared budget must come back,
	// otherwise concurrency looks like a storm.
	only.Release()
	if slot, reason := b.Reserve("gw=openai"); slot == nil {
		t.Fatalf("after release: %s", reason)
	}
	only.Release() // idempotent
}

func TestResetClearsCooldownAfterTheDependencyWorksAgain(t *testing.T) {
	b, c := newTestBrake(12)
	first, _ := b.Reserve("gw=openai")
	second, _ := b.Reserve("gw=openai")
	if second.At().Before(c.now.Add(30 * time.Second)) {
		t.Fatal("fixture did not space the second slot")
	}
	b.Reset("gw=openai")
	third, _ := b.Reserve("gw=openai")
	if !third.At().Equal(c.now) {
		t.Fatalf("slot after reset = %v, want now", third.At())
	}
	_ = first
}

// A storm is exactly this shape: many callers at once. The gate must never
// over-admit, and it must never hand out the same instant to two callers.
func TestConcurrentReservationsNeverOverAdmit(t *testing.T) {
	b, _ := newTestBrake(5)
	var mu sync.Mutex
	slots := []*Slot{}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if slot, _ := b.Reserve("gw=openai"); slot != nil {
				mu.Lock()
				slots = append(slots, slot)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if len(slots) != 5 {
		t.Fatalf("granted %d slots, want exactly the cap 5", len(slots))
	}
	seen := map[time.Time]bool{}
	for _, slot := range slots {
		if seen[slot.At()] {
			t.Fatalf("two callers were given the same instant %v", slot.At())
		}
		seen[slot.At()] = true
	}
}
