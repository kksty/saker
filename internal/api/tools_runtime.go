package api

import (
	"context"
	"net/http"
	"sort"
	"time"

	"github.com/kksty/saker/internal/aop"
	"github.com/kksty/saker/internal/executor"
)

// runtimeCatalogueTimeout bounds one AOP ListAgents read. The host is local, so
// a reachable one answers quickly; a dead one must fail in bounded time and
// explain itself instead of hanging a page.
const runtimeCatalogueTimeout = 5 * time.Second

type runtimeToolsResponse struct {
	State      string            `json:"state"`
	Error      string            `json:"error,omitempty"`
	AgentID    string            `json:"agent_id,omitempty"`
	Provider   string            `json:"provider,omitempty"`
	Model      string            `json:"model,omitempty"`
	ModelError string            `json:"model_error,omitempty"`
	Commands   []aop.CommandSpec `json:"commands"`
	CheckedAt  time.Time         `json:"checked_at"`
}

// listRuntimeTools reads the running default host's own catalogue. It never
// launches a profile, opens a session or probes a target. Keep it separate
// from log statistics so a slow host cannot block their normal polling.
func (s *Server) listRuntimeTools(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	out := s.runtimeCatalogue(r.Context(), runtimeCatalogueTimeout)
	writeJSON(w, http.StatusOK, out)
}

// runtimeCatalogue is the one implementation behind /api/tools/runtime and the
// runtime section of /api/tools: the host's declared commands/skills plus its
// model health. Availability is this declaration, not a file on disk. When the
// host is unreachable the response keeps state="unavailable" and an error, so
// callers render a reason instead of an empty successful list.
func (s *Server) runtimeCatalogue(ctx context.Context, timeout time.Duration) runtimeToolsResponse {
	out := runtimeToolsResponse{State: "unavailable", Commands: []aop.CommandSpec{}, CheckedAt: time.Now()}
	tn, ok := s.Exec.(executor.TerminalNode)
	if !ok {
		out.Error = "执行器没有本机宿主通道"
		return out
	}
	base, token, agentID, ok := tn.TerminalEndpoint(s.localNode())
	if !ok {
		out.Error = "本机宿主未配置"
		return out
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	agents, err := aop.New(base, token).Agents(ctx)
	if err != nil {
		out.Error = s.redactSecrets(err.Error())
	} else if agent, found := aop.Find(agents, agentID); !found {
		out.Error = "宿主未注册指定的执行者"
	} else {
		out.State, out.AgentID = "connected", agent.ID
		out.Provider, out.Model = agent.Provider, agent.Model
		out.ModelError = s.redactSecrets(agent.ConfigError)
		for _, cmd := range agent.Commands {
			if cmd.Name != "" {
				out.Commands = append(out.Commands, cmd)
			}
		}
		sort.Slice(out.Commands, func(i, j int) bool { return out.Commands[i].Name < out.Commands[j].Name })
	}
	return out
}
