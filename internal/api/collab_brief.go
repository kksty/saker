package api

import (
	"fmt"
	"strings"

	"github.com/kksty/saker/internal/collab"
	"github.com/kksty/saker/internal/model"
)

const collabBriefMaxPeers = 6

func (s *Server) collaborationSection(self model.Intent) string {
	var peers []string
	for _, in := range s.Store.ListIntents() {
		if in.ID == self.ID {
			continue
		}
		switch in.Status {
		case model.StatusDone, model.StatusFailed, model.StatusReviewFailed, model.StatusParked:
			continue // no live holder: a message would be queued forever
		}
		peers = append(peers, fmt.Sprintf("%s（%s，%s）", clipRunes(in.Title, 40), collab.ParticipantID(in.ID), in.Status))
		if len(peers) >= collabBriefMaxPeers {
			break
		}
	}
	var b strings.Builder
	b.WriteString("\n\n## 自主协作（不必经过指挥官）\n")
	if len(peers) > 0 {
		b.WriteString("- 同伴：" + strings.Join(peers, "；") + "\n")
	}
	b.WriteString("- 可用动作：给同伴或指挥官发消息、共享已登记的事实、申请接班。\n")
	b.WriteString("- 现场交接：可直接使用意图/插话提供的账号登录；扫码、验证码或人工确认时，可随人工请求关联自己的可见浏览器 session，宿主自动跟随本轮；人工完成后继续。多站点按 URL 选择页面，同站点多账号使用独立浏览器上下文。原生结果可按需查询。\n")
	b.WriteString("- 契约与身份：harness skill `collaborate`（`cyber://skills/collaborate/SKILL.md`），")
	b.WriteString("本轮身份在 `.tw/identity.json`。\n")
	return b.String()
}
