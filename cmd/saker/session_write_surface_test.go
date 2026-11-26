package main

import "testing"

// The session write surface is an allowlist, not a denial list: a write route
// added to the API later must stay 404 for harness sessions until it is named
// here. This pins the matcher (including the "*" segment normalization) so a new
// handler cannot become reachable by simply not being in an old deny list.
func TestSessionWriteAllowlistIsDefaultDeny(t *testing.T) {
	for _, tc := range []struct {
		method, path string
		want         bool
	}{
		// Documented commander writes.
		{"POST", "/p/p1/api/intents", true},
		{"POST", "/p/p1/api/intents/in_1/run", true},
		{"POST", "/p/p1/api/intents/in_1/say", true},
		{"POST", "/p/p1/api/intents/in_1/operator", true},
		{"PUT", "/p/p1/api/intents/in_1/context", true},
		{"POST", "/p/p1/api/goals/g_1/update", true},
		{"POST", "/p/p1/api/goals/g_1/complete", true},
		{"POST", "/p/p1/api/messages/m_1/retract", true},
		{"POST", "/p/p1/api/operator-requests", true},
		{"PUT", "/p/p1/api/operator-requests/or_1", true},
		// The commander decides proposals on the project face; filing and
		// withdrawing one stay participant-only (/agent), so those are absent here.
		{"POST", "/p/p1/api/proposals/pr_1/approve", true},
		{"POST", "/p/p1/api/proposals/pr_1/reject", true},
		{"POST", "/p/p1/api/proposals/pr_1/dispatch", true},
		{"PUT", "/p/p1/api/state", true},
		{"POST", "/p/p1/api/project/standby", true},
		// Operator-only writes are not named, so they stay invisible.
		{"PUT", "/p/p1/api/documents/scope", false},
		{"POST", "/p/p1/api/goals/g_1/met", false},
		{"POST", "/p/p1/api/project/pause", false},
		{"POST", "/p/p1/api/proposals", false},
		{"POST", "/p/p1/api/proposals/pr_1/withdraw", false},
		{"PUT", "/api/settings", false},
		{"POST", "/p/p1/api/instructions", false},
		{"PUT", "/p/p1/api/instructions/instruction_1", false},
		{"DELETE", "/p/p1/api/playbooks/pb_1", false},
		{"POST", "/p/p1/api/reports", false},
	} {
		if got := sessionWriteAllowed(tc.method, tc.path); got != tc.want {
			t.Errorf("sessionWriteAllowed(%s %s) = %v, want %v", tc.method, tc.path, got, tc.want)
		}
	}
}
