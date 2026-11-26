package aop

import "testing"

// TestExtensionTypeURLsMatchTheHarnessFork pins the three extension type URLs
// against the strings the harness fork matches literally
// (cyber-harness agent/session/protocol.go: workDirExtensionURL,
// runGoalExtensionURL, toolEnvExtensionURL).
//
// The fork compares TypeUrl for equality and continues on a mismatch, so a
// wrong value here is not an error anywhere: the session silently keeps the
// agent's process directory, the evaluator judges the executor's brief instead
// of the intent, and tool subprocesses never receive SAKER_*. Nothing is logged,
// which is why this belongs in a unit test rather than in a run.
func TestExtensionTypeURLsMatchTheHarnessFork(t *testing.T) {
	for _, tc := range []struct {
		name string
		got  string
		want string
	}{
		{"WorkDir", WorkDirExtensionURL, "type.googleapis.com/saker.WorkDir"},
		{"RunGoal", RunGoalExtensionURL, "type.googleapis.com/saker.RunGoal"},
		{"ToolEnv", ToolEnvExtensionURL, "type.googleapis.com/saker.ToolEnv"},
	} {
		if tc.got != tc.want {
			t.Errorf("%s extension URL = %q, want %q: the harness fork matches this string literally and ignores any other value without an error or a log line", tc.name, tc.got, tc.want)
		}
	}
}
