package api

import (
	"crypto/rand"
	"encoding/hex"
	"github.com/kksty/saker/internal/executor"
	"net/http"
	"net/url"
)

// Only the operator explicitly opening setup receives the Hub shell credential.
// It never rides the general node list or a session's capability surface.
func (s *Server) nodeJoin(w http.ResponseWriter, r *http.Request) {
	if harnessSession(r) {
		writeErr(w, 404, "not found")
		return
	}
	provider, ok := s.Exec.(executor.TerminalNode)
	if !ok {
		writeErr(w, 409, "没有 Hub")
		return
	}
	base, token, _, ok := provider.TerminalEndpoint(s.localNode())
	if !ok {
		writeErr(w, 409, "没有本机 Hub")
		return
	}
	u, err := url.Parse(base)
	if err != nil {
		writeErr(w, 500, "Hub 地址无效")
		return
	}
	u.User = url.User(token)
	var nodeID [16]byte
	if _, err := rand.Read(nodeID[:]); err != nil {
		writeErr(w, 500, "无法生成节点标识")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, map[string]any{"server_url": u.String(), "args": []string{"agent", "--transport", "web", "--server-url", u.String(), "--node-id", hex.EncodeToString(nodeID[:]), "--node-name", "worker", "--observe", "http", "--timeout", "0"}, "workspace": s.CaseRoot, "note": "在共享数据目录启动 scan；跨机器时将 server-url 主机改成 Hub 可达地址，保留凭据和 node-id 供重连复用。Hub 需监听该地址；节点须共享项目文件，--agent-api-base 指定的控制面 API 也必须可达。"})
}
