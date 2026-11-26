package api

import (
	"context"

	"github.com/kksty/saker/internal/executor"
)

// DefaultModelExecutor gives auxiliary calls the same host readiness boundary
// as workers and commander rounds. Resolve at the actual call, after any wait
// for auxiliary capacity, so queued work observes the latest pause state.
func (s *Server) DefaultModelExecutor(ex executor.Executor) executor.Executor {
	return defaultModelExecutor{Executor: ex, server: s}
}

type defaultModelExecutor struct {
	executor.Executor
	server *Server
}

func (e defaultModelExecutor) Nodes() []executor.NodeInfo {
	if na, ok := e.Executor.(executor.NodeAware); ok {
		return na.Nodes()
	}
	return []executor.NodeInfo{{ID: executor.ResolveNode(e.Executor, ""), Default: true}}
}

func (e defaultModelExecutor) ResolveNode(requested string) string {
	return executor.ResolveNode(e.Executor, requested)
}

func (e defaultModelExecutor) Run(ctx context.Context, req executor.Request, emit func(executor.Event)) (executor.Result, error) {
	if executor.ResolveNode(e.Executor, req.Node) == e.server.localNode() {
		if err := e.server.CheckLocalHost(); err != nil {
			return executor.Result{ExitCode: -1}, err
		}
	}
	return e.Executor.Run(ctx, req, emit)
}
