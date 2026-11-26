package aop

import (
	"encoding/json"
	"strings"

	"google.golang.org/protobuf/types/known/anypb"
)

// ToolEnvExtensionURL is the per-turn tool environment extension on
// RunTurnRequest. The harness runs a session's shell in an embedded interpreter
// that does not source BASH_ENV/ENV, so a session init script alone never sets
// the caller's SAKER_* variables and the model falls back to guessing endpoints.
// This extension carries the absolute values instead: the fork merges them into
// every tool subprocess of the turn. An old harness ignores the unknown
// extension, so sending it is additive.
//
// The type name is a wire contract with another process; see
// WorkDirExtensionURL for why it is not renamed with the product.
const ToolEnvExtensionURL = "type.googleapis.com/saker.ToolEnv"

// DisableNativeIOASendEnv controls the harness command without disabling its IOA connection.
const DisableNativeIOASendEnv = "CYBER_IOA_SEND_DISABLED"

type toolEnvPayload struct {
	Env map[string]string `json:"env"`
}

// ToolEnvExtension encodes the environment every tool subprocess of a turn
// should inherit. It returns nil when nothing is set so a turn without an
// environment keeps exactly the previous behavior on the wire.
func ToolEnvExtension(env map[string]string) *anypb.Any {
	clean := make(map[string]string, len(env))
	for key, value := range env {
		if strings.TrimSpace(key) != "" {
			clean[key] = value
		}
	}
	if len(clean) == 0 {
		return nil
	}
	raw, err := json.Marshal(toolEnvPayload{Env: clean})
	if err != nil {
		return nil
	}
	return &anypb.Any{TypeUrl: ToolEnvExtensionURL, Value: raw}
}
