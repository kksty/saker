package aop

import (
	"encoding/json"
	"strings"

	"google.golang.org/protobuf/types/known/anypb"
)

// WorkDirExtensionURL is the harness fork's per-session workspace extension on
// OpenSessionRequest. Upstream AOP has no session working directory, and one
// agent process serves every run, so without this a session resolves relative
// paths against the agent's process directory (saker's <data>). The fork reads
// the value (JSON with work_dir and single_task) and binds the workspace plus
// the native mailbox scheduling policy (D110, D129).
//
// The type name is a wire contract with another process, not a product name, so
// it keeps the spelling that process matches literally: cyber-harness
// agent/session/protocol.go, workDirExtensionURL. A different value is not
// rejected either: the fork skips an unknown extension without an error or a
// log line, the session quietly keeps the agent's process directory, and every
// relative path a run writes lands there instead of the case directory. Change
// both sides together, or neither.
const WorkDirExtensionURL = "type.googleapis.com/saker.WorkDir"

type workDirPayload struct {
	WorkDir    string `json:"work_dir"`
	SingleTask bool   `json:"single_task"`
}

// WorkDirExtension encodes one session's workspace. Empty returns nil, which
// keeps the upstream behavior (agent process directory) for callers without a
// case.
func WorkDirExtension(dir string) *anypb.Any {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return nil
	}
	// The control plane owns turn admission. Native messages may enter the
	// mailbox, but only a tracked RunTurn may start model execution.
	raw, err := json.Marshal(workDirPayload{WorkDir: dir, SingleTask: true})
	if err != nil {
		return nil
	}
	return &anypb.Any{TypeUrl: WorkDirExtensionURL, Value: raw}
}
