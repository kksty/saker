package verifier

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kksty/saker/internal/executor"
)

// captureCredentialExec records what the reviewer's work dir and brief looked
// like at the moment the run started, then writes the verdict the verifier
// expects.
type captureCredentialExec struct {
	workDir  string
	prompt   string
	auth     string
	authErr  error
	identity string
	idErr    error
}

func (c *captureCredentialExec) Name() string { return "capture" }

func (c *captureCredentialExec) Run(ctx context.Context, req executor.Request, emit func(executor.Event)) (executor.Result, error) {
	c.workDir = req.WorkDir
	c.prompt = req.Prompt
	auth, err := os.ReadFile(filepath.Join(req.WorkDir, ".saker", "auth"))
	c.auth, c.authErr = string(auth), err
	id, err := os.ReadFile(filepath.Join(req.WorkDir, ".saker", "identity.json"))
	c.identity, c.idErr = string(id), err
	if err := os.WriteFile(filepath.Join(req.WorkDir, VerdictPath), []byte(`{"verdict":"pass","notes":"重跑一致","proof":"curl /admin → 200"}`), 0o644); err != nil {
		return executor.Result{ExitCode: 1}, err
	}
	return executor.Result{ExitCode: 0}, nil
}

// The reviewer's work dir is new and empty, and its brief names the proof as a
// path under the case root. Handing it a read-only credential is what makes those
// bytes reachable at all; leaving the credential behind after the run would let a
// finished review keep reading the project it just judged.
func TestReviewRunGetsAReadOnlyCredentialForItsOwnLifetime(t *testing.T) {
	st, f, root := setup(t)
	ex := &captureCredentialExec{}
	v := New(st, ex, root)
	var issued, released []string
	v.IssueRead = func(runID string) (ReadCredential, error) {
		issued = append(issued, runID)
		return ReadCredential{APIBase: "http://127.0.0.1:8080", ProjectID: "p-1", Token: "tok-read"}, nil
	}
	v.ReleaseRead = func(runID string) {
		released = append(released, runID)
	}

	review, err := v.Run(context.Background(), f.ID)
	if err != nil {
		t.Fatalf("review run: %v", err)
	}
	if review.Verdict != "pass" {
		t.Fatalf("verdict = %q, want pass", review.Verdict)
	}

	if ex.authErr != nil {
		t.Fatalf("reviewer could not read .saker/auth: %v", ex.authErr)
	}
	// Exactly the line the shell contract documents for $SAKER_AUTH.
	if ex.auth != "Authorization: Bearer tok-read\n" {
		t.Fatalf(".saker/auth = %q", ex.auth)
	}
	if ex.idErr != nil {
		t.Fatalf("reviewer could not read .saker/identity.json: %v", ex.idErr)
	}
	for _, want := range []string{`"api_base": "http://127.0.0.1:8080"`, `"project_id": "p-1"`, `"intent_id": "` + f.IntentID + `"`} {
		if !strings.Contains(ex.identity, want) {
			t.Errorf("identity.json missing %s: %s", want, ex.identity)
		}
	}
	// The brief has to name the surface, or the credential is invisible to the
	// model and every review ends in needs_evidence.
	if !strings.Contains(ex.prompt, "$SAKER_BATTLE") || !strings.Contains(ex.prompt, "/evidence/{id}") {
		t.Errorf("review brief does not name the read-only surface")
	}

	if len(issued) != 1 || len(released) != 1 || issued[0] != released[0] {
		t.Fatalf("issued=%v released=%v, want one matching pair", issued, released)
	}
	// The token is dead after ReleaseRead; the files that carried it must not
	// survive it either.
	for _, name := range []string{"identity.json", "auth"} {
		if _, err := os.Stat(filepath.Join(ex.workDir, ".saker", name)); !os.IsNotExist(err) {
			t.Errorf(".saker/%s still present after the run (err=%v)", name, err)
		}
	}
}
