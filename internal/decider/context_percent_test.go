package decider

import "testing"

// The wire status uses a pointer so "not observed" and "observed 0%" are
// different values. The console must not print a fabricated 0 for a missing
// observation, and must print 0 for a real sub-percent value.
func TestContextPercentDistinguishesUnknownFromKnownZero(t *testing.T) {
	if got := (ContextStatus{Used: 0, Window: 1000}).PercentPtr(); got != nil {
		t.Fatalf("missing observation = %v, want nil", *got)
	}
	if got := (ContextStatus{Used: 100, Window: 1000}).PercentPtr(); got == nil || *got != 10 {
		t.Fatalf("10%% = %v", got)
	}
	if got := (ContextStatus{Used: 1, Window: 1000}).PercentPtr(); got == nil || *got != 0 {
		t.Fatalf("real sub-percent zero = %v, want a present 0", got)
	}
}
