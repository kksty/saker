package settings

import (
	"errors"
	"os"
	"strings"
	"testing"
)

func TestSaveGuardrailValidatesCandidateBeforeWrite(t *testing.T) {
	// A rejected scan candidate used to be discoverable only at startup.
	// The editor must leave the saved file intact on rejection.
	s := guardrailStore(t, "jev:\n  api_key: fixture-jev-secret\n")
	before, err := os.ReadFile(s.harness)
	if err != nil {
		t.Fatal(err)
	}
	called := false
	changed, err := s.SaveGuardrail(GuardrailInput{Provider: "jev"}, func(candidate []byte) error {
		called = true
		if !strings.Contains(string(candidate), "provider: jev") || !strings.Contains(string(candidate), "fixture-jev-secret") {
			t.Fatal("offline validator did not get the candidate and preserved credential")
		}
		return errors.New("fixture rejection")
	})
	if !called || changed || !errors.Is(err, ErrGuardrailConfig) {
		t.Fatalf("save after rejection: changed=%v err=%v", changed, err)
	}
	after, err := os.ReadFile(s.harness)
	if err != nil || string(before) != string(after) {
		t.Fatalf("rejected save changed the file: %v", err)
	}
}
