package executor

import (
	"fmt"
	"strings"

	"github.com/kksty/saker/internal/model"
)

// InstructionSection renders the commander’s saved operator messages.
func InstructionSection(rows []model.Instruction) string {
	if len(rows) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n## 当前持续指令\n以下是操作员保存的偏好、待办和提醒。\n")
	for _, in := range rows {
		scope := "项目"
		if in.IntentID != "" {
			scope = "关联意图 " + in.IntentID
		}
		fmt.Fprintf(&b, "\n### %s · v%d · %s\n%s\n", in.ID, in.Version, scope, in.Text)
	}
	return b.String()
}
