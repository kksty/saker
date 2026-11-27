package api

import (
	"fmt"
	"strings"

	"github.com/kksty/saker/internal/collab"
	"github.com/kksty/saker/internal/model"
)

const collabBriefMaxPeers = 6

// collabBriefPeerFacts caps the recent fact titles carried per peer. The line
// exists so "should I ask A" costs one read instead of a peek at A's run
// events; more than a couple of titles turns the brief into a second fact list.
const collabBriefPeerFacts = 2

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
		line := fmt.Sprintf("%s（%s，%s）", clipRunes(in.Title, 40), collab.ParticipantID(in.ID), in.Status)
		peers = append(peers, line+peerFactsSuffix(s.peerFacts(in.ID)))
		if len(peers) >= collabBriefMaxPeers {
			break
		}
	}
	var b strings.Builder
	b.WriteString("\n\n## 自主协作（不必经过指挥官）\n")
	if len(peers) > 0 {
		b.WriteString("- 同伴：\n")
		for _, line := range peers {
			b.WriteString("  - " + line + "\n")
		}
	}
	b.WriteString("- 可用动作：给同伴或指挥官发消息、共享已登记的事实、申请接班。\n")
	b.WriteString("- 现场交接：可直接使用意图/插话提供的账号登录；扫码、验证码或人工确认时，可随人工请求关联自己的可见浏览器 session，宿主自动跟随本轮；人工完成后继续。多站点按 URL 选择页面，同站点多账号使用独立浏览器上下文。原生结果可按需查询。\n")
	b.WriteString("- 契约与身份：harness skill `collaborate`（`cyber://skills/collaborate/SKILL.md`），")
	b.WriteString("本轮身份在 `.saker/identity.json`。\n")
	return b.String()
}

// peerFacts is what a peer has actually registered, newest first. Withdrawn and
// rejected rows are skipped rather than counted: a retracted assertion is not
// something to ask about, and one filled slot by a retracted title would hide
// the row the peer still stands behind.
func (s *Server) peerFacts(intentID string) []model.Fact {
	usable := make([]model.Fact, 0, collabBriefPeerFacts)
	for _, f := range s.Store.ListFacts(intentID, 0) {
		if f.Status == "withdrawn" || f.Status == "rejected" {
			continue
		}
		usable = append(usable, f)
		if len(usable) == collabBriefPeerFacts {
			break
		}
	}
	return usable
}

// peerFactsSuffix renders those titles as a peer-line tail. Titles only: the
// body stays behind its intent id and version, and an unverified row keeps its
// label so a title is never read as an established conclusion.
func peerFactsSuffix(facts []model.Fact) string {
	if len(facts) == 0 {
		return "：还没有登记事实"
	}
	parts := make([]string, 0, len(facts))
	for _, f := range facts {
		title := "「" + clipRunes(f.Title, 40) + "」"
		if f.Status != "verified" {
			title += "（未核验）"
		}
		parts = append(parts, title)
	}
	return "：最近事实" + strings.Join(parts, "、")
}
