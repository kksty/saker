package main

import (
	"net/http"
	"testing"
)

// The commander's goal writes need their run identity attached by the gate:
// update/close re-check operator provenance, complete records MetByRun, and
// reopen decides whether the session may withdraw the stamp. A path that slips
// out of this list silently loses that distinction (the handler then sees only
// "a session"), so pin the whole family plus the two paths that must stay out.
func TestGoalWritesCarryRunIdentity(t *testing.T) {
	cases := []struct {
		method, path string
		want         bool
	}{
		{http.MethodPost, "/api/goals", true},
		{http.MethodPost, "/api/goals/g_1/update", true},
		{http.MethodPost, "/api/goals/g_1/close", true},
		{http.MethodPost, "/api/goals/g_1/complete", true},
		{http.MethodPost, "/api/goals/g_1/reopen", true},
		{http.MethodPost, "/api/goals/g_1/met", false},   // operator-only pin
		{http.MethodGet, "/api/goals/g_1/complete", false},
		{http.MethodPost, "/api/intents/in_1/run", false},
		{http.MethodPost, "/api/messages", false},
	}
	for _, c := range cases {
		if got := goalWriteCarriesRunIdentity(c.method, c.path); got != c.want {
			t.Errorf("goalWriteCarriesRunIdentity(%s %s) = %v, want %v", c.method, c.path, got, c.want)
		}
	}
}
