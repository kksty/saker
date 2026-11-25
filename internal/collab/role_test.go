package collab

import "testing"

// RoleOf is the one provenance classifier: the console and the board read the
// same role, so its boundary cases are pinned here. Provenance is not permission:
// an executor id classifies as the executor role whatever created the row.
func TestRoleOfClassifiesParticipantIDs(t *testing.T) {
	cases := []struct {
		name        string
		participant string
		want        Role
	}{
		{"empty", "", RoleUnknown},
		{"commander exact", CommanderID, RoleCommander},
		{"operator exact", OperatorID, RoleOperator},
		{"executor from intent", ParticipantID("in_abc"), RoleExecutor},
		{"executor literal", "pt_in_abc", RoleExecutor},
		{"executor non-in intent id", "pt_x", RoleExecutor},
		{"bare pt prefix", "pt_", RoleUnknown},
		{"no underscore", "pt", RoleUnknown},
		{"commander with suffix", CommanderID + "x", RoleUnknown},
		{"commander different case", "Commander", RoleUnknown},
		{"operator with suffix", OperatorID + "2", RoleUnknown},
		{"raw intent id", "in_abc", RoleUnknown},
		{"mechanical sender", "ledger", RoleUnknown},
		{"padded id", " " + CommanderID + " ", RoleUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := RoleOf(tc.participant); got != tc.want {
				t.Fatalf("RoleOf(%q) = %q, want %q", tc.participant, got, tc.want)
			}
		})
	}
}

// The role strings cross the API boundary (board JSON, console, prompt identity
// sections), so the wire values are a contract, not an implementation detail.
func TestRoleWireValuesAreStable(t *testing.T) {
	want := map[Role]string{
		RoleCommander: "commander",
		RoleOperator:  "operator",
		RoleExecutor:  "executor",
		RoleUnknown:   "unknown",
	}
	if len(want) != 4 {
		t.Fatalf("role set changed: %d constants", len(want))
	}
	for role, value := range want {
		if string(role) != value {
			t.Fatalf("role %v wire value = %q, want %q", role, string(role), value)
		}
	}
}
