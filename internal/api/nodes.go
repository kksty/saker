package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/kksty/saker/internal/executor"
	"github.com/kksty/saker/internal/host"
)

// nodeView projects configured execution locations and native scan discovery.
type nodeView struct {
	Agent       string `json:"agent,omitempty"`
	Name        string `json:"name,omitempty"`
	Online      *bool  `json:"online,omitempty"`
	Ready       *bool  `json:"ready,omitempty"`
	WorkingDir  string `json:"working_dir,omitempty"`
	Model       string `json:"model,omitempty"`
	ConfigError string `json:"config_error,omitempty"`
	ID          string `json:"id"`
	Note        string `json:"note,omitempty"`
	Default     bool   `json:"default,omitempty"`
	Transport   string `json:"transport,omitempty"`
	Endpoint    string `json:"endpoint,omitempty"`
	Bin         string `json:"bin,omitempty"`
	Terminal    bool   `json:"terminal,omitempty"`
	// Bound is the project allowlist; ProjectDefault is its dispatch default.
	Bound          bool             `json:"bound"`
	ProjectDefault bool             `json:"project_default,omitempty"`
	Local          bool             `json:"local,omitempty"`
	Host           *host.HostStatus `json:"host,omitempty"`
}

// listNodes refreshes the native Hub registry and retains cached offline nodes.
func (s *Server) listNodes(w http.ResponseWriter, r *http.Request) {
	var discoveryError string
	if discovery, ok := s.Exec.(executor.NodeDiscovery); ok {
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		if err := discovery.RefreshNodes(ctx); err != nil {
			discoveryError = err.Error()
		}
	}
	_, def := s.nodeBinding()
	writeJSON(w, http.StatusOK, map[string]any{
		"nodes":           s.nodeViews(),
		"default":         s.localNode(),
		"project_default": def,
		"discovery_error": discoveryError,
	})
}

// setNodes persists operator-managed project placement.
func (s *Server) setNodes(w http.ResponseWriter, r *http.Request) {
	if harnessSession(r) {
		writeErr(w, 403, "节点绑定由操作员管理")
		return
	}
	if s.Project == nil {
		writeErr(w, 409, "没有项目")
		return
	}
	var req struct {
		Nodes   []string `json:"nodes"`
		Default string   `json:"default"`
	}
	if json.NewDecoder(r.Body).Decode(&req) != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	for _, id := range req.Nodes {
		if !s.HasNode(strings.TrimSpace(id)) {
			writeErr(w, 400, "节点不存在："+id)
			return
		}
	}
	if err := s.Project.SetNodeBinding(req.Nodes, req.Default); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	s.listNodes(w, r)
}

func (s *Server) nodeBinding() ([]string, string) {
	if s.Project != nil && s.Project.BindingConfigured() {
		return s.Project.NodeBinding()
	}
	return []string{s.localNode()}, s.localNode()
}

func (s *Server) nodeViews() []nodeView {
	na, ok := s.Exec.(executor.NodeAware)
	if !ok {
		return []nodeView{}
	}
	local := s.localNode()
	bound, def := s.nodeBinding()
	infos := na.Nodes()
	out := make([]nodeView, 0, len(infos))
	for _, n := range infos {
		view := nodeView{
			Agent: n.Agent, Name: n.Name, Online: n.Online, Ready: n.Ready, WorkingDir: n.WorkingDir, Model: n.Model, ConfigError: n.ConfigError,
			ID:             n.ID,
			Note:           n.Note,
			Default:        n.Default || n.ID == local,
			Transport:      n.Transport,
			Endpoint:       n.Endpoint,
			Bin:            n.Bin,
			Terminal:       n.Transport == "aop",
			Bound:          containsNode(bound, n.ID),
			ProjectDefault: n.ID == def,
			Local:          n.ID == local,
		}
		if n.ID == local && s.LocalHostStatus != nil {
			status := s.LocalHostStatus()
			view.Host = &status
		}
		out = append(out, view)
	}
	return out
}

// CheckLocalHost is the shared default-model dispatch gate. Preserve the
// supervisor's reason instead of turning every failure into a binding error.
func (s *Server) CheckLocalHost() error {
	if s.LocalHostStatus == nil {
		return nil
	}
	status := s.LocalHostStatus()
	if status.Ready {
		return nil
	}
	reason := status.LastError
	if reason == "" {
		if status.Online {
			reason = "模型正在初始化"
		} else {
			reason = "执行宿主正在启动或已停止"
		}
	}
	return fmt.Errorf("本机宿主未就绪：%s；请在「执行」查看状态，修好模型连接后在「设置 → 模型」保存重试", reason)
}

// localNode identifies the paired scan managed by this control plane.
func (s *Server) localNode() string {
	if s.LocalNode != "" {
		return s.LocalNode
	}
	// A non-NodeAware executor is the single-host case: ResolveNode reports
	// "local" for an empty request, which is exactly this edition.
	if id := executor.ResolveNode(s.Exec, ""); id != "" {
		return id
	}
	na, ok := s.Exec.(executor.NodeAware)
	if !ok {
		return ""
	}
	for _, n := range na.Nodes() {
		if n.Default {
			return n.ID
		}
	}
	if nodes := na.Nodes(); len(nodes) > 0 {
		return nodes[0].ID
	}
	return ""
}

// checkNode requires an explicitly requested node to exist and be bound.
func (s *Server) checkNode(id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil
	}
	if !s.HasNode(id) {
		return fmt.Errorf("节点 %s 不存在", id)
	}
	bound, _ := s.nodeBinding()
	if !containsNode(bound, id) {
		return fmt.Errorf("节点 %s 未绑定到项目，请在执行页启用", id)
	}
	return nil
}

// resolveNode applies the project default and dispatch-time readiness checks.
func (s *Server) resolveNode(requested string) (string, error) {
	id := strings.TrimSpace(requested)
	if id == "" {
		_, id = s.nodeBinding()
	}
	if id == "" {
		return "", errors.New("项目没有默认执行节点")
	}
	if err := s.checkNode(id); err != nil {
		return "", err
	}
	if na, ok := s.Exec.(executor.NodeAware); ok {
		for _, n := range na.Nodes() {
			if n.ID == id {
				if n.Online != nil && !*n.Online {
					return "", fmt.Errorf("节点 %s 已离线", id)
				}
				if n.Ready != nil && !*n.Ready {
					return "", fmt.Errorf("节点 %s 未就绪：%s", id, n.ConfigError)
				}
			}
		}
	}
	return id, nil
}

func containsNode(ids []string, id string) bool {
	for _, candidate := range ids {
		if candidate == id {
			return true
		}
	}
	return false
}
