package aopexec

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/kksty/saker/internal/aop"
)

// sessionToolEnv computes the environment a session's shell needs to reach the
// control plane, from the case directory's .tw state the dispatcher already
// writes for the turn. The values are absolute: the host has no reliable notion
// of the control-plane base, and asking the model to walk up from $PWD is what
// produced guessed URLs and the 404 storm.
//
// It returns nil, not a half-built environment, when the case has no identity
// for this turn: a missing SAKER_API must read as "not set", never as an empty
// string the model could mistake for a value. Values are never logged.
func sessionToolEnv(caseDir string) map[string]string {
	if strings.TrimSpace(caseDir) == "" {
		return nil
	}
	twDir := filepath.Join(caseDir, ".tw")
	raw, err := os.ReadFile(filepath.Join(twDir, "identity.json"))
	if err != nil {
		return nil
	}
	var id struct {
		APIBase   string `json:"api_base"`
		ProjectID string `json:"project_id"`
		IntentID  string `json:"intent_id"`
	}
	if err := json.Unmarshal(raw, &id); err != nil {
		return nil
	}
	api := strings.TrimRight(strings.TrimSpace(id.APIBase), "/")
	project := strings.TrimSpace(id.ProjectID)
	if api == "" || project == "" {
		return nil
	}
	env := map[string]string{
		"SAKER_API":                    api,
		"SAKER_PROJECT":                project,
		"SAKER_SELF":                   strings.TrimSpace(id.IntentID),
		"SAKER_AGENT":                  api + "/agent/" + project + "/api",
		"SAKER_BATTLE":                 api + "/p/" + project + "/api",
		aop.DisableNativeIOASendEnv: "1",
	}
	// .tw/auth holds the ready-to-use "Authorization: Bearer …" line, which is
	// exactly what $SAKER_AUTH is documented to be. Reading it (instead of the raw
	// token in identity.json) keeps the shell contract identical to the init
	// script's fallback.
	if auth, err := os.ReadFile(filepath.Join(twDir, "auth")); err == nil {
		if line := strings.TrimSpace(string(auth)); line != "" {
			env["SAKER_AUTH"] = line
		}
	}
	if bin, err := os.Executable(); err == nil {
		if abs, absErr := filepath.Abs(bin); absErr == nil {
			bin = abs
		}
		env["SAKER_BIN"] = filepath.ToSlash(bin)
	}
	return env
}
