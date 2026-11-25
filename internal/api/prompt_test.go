package api

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/kksty/tianwu/internal/board"
	"github.com/kksty/tianwu/internal/collab"
	"github.com/kksty/tianwu/internal/contract"
	"github.com/kksty/tianwu/internal/decider"
	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/store"
)

func testDB() store.DB {
	return store.DB{
		Intents: []model.Intent{
			{
				ID:          "in_new",
				Title:       "验证 /admin 越权",
				Description: "普通用户能否读到全部订单",
				Doc:         "用 uid=2 的 cookie 请求 GET /admin",
				FromFactIDs: []string{"f_1"},
			},
			{ID: "in_old", Title: "扫端口", Status: model.StatusFailed},
			{ID: "in_park", Title: "测 SSRF", Status: model.StatusParked, ParkReason: "需要内网出口"},
			{ID: "in_run", Title: "测登录限速", Status: model.StatusRunning},
		},
		Facts: []model.Fact{{
			ID:         "f_1",
			Kind:       "host",
			Title:      "10.0.0.5 开放 8080",
			Summary:    "tomcat 9.0.31",
			Doc:        "curl -s http://10.0.0.5:8080/ → Server: Apache-Coyote/1.1",
			Confidence: "observed",
			Origin:     model.OriginDeclared,
		}},
		Goals: []model.Goal{{Statement: "拿到管理员权限", QuantifiedTarget: "至少一条 verified 的 vault fact"}},
	}
}

// A dispatch names its premises by fact id. When the ledger cannot resolve one,
// the brief must say the premise is unproven — not that it may be ignored, which
// is the same as defaulting an unknown to "does not matter".
func TestMissingSourceFactIsNotSilentlyIgnorable(t *testing.T) {
	db := testDB()
	db.Intents[0].FromFactIDs = []string{"f_missing"}
	p := BuildPrompt(db, db.Intents[0], "", "", "", 0, false)
	for _, want := range []string{"前提无法核对", "按未成立处理", "不得当已确认前提"} {
		if !strings.Contains(p, want) {
			t.Fatalf("missing premise is not stated plainly (%q):\n%s", want, p)
		}
	}
	if strings.Contains(p, "可忽略") {
		t.Fatalf("missing premise still reads as ignorable:\n%s", p)
	}
}

// The opening leaves judgment with the executor for every source, including old rows.
const expectedExecutorOpening = "你是执行者。下面的意图是发起者给出的下一步：它说明这一步要做什么、怎样算完成，不规定你怎么做。照做、改造、换路或上报由你按现场判断，平台不校验你是否照做；派发者附带的路线与步骤只是参考，不是完成条件。判断与理由写进交接。"

func TestBuildPromptSections(t *testing.T) {
	db := testDB()
	db.Facts[0].Status = "verified"
	p := BuildPrompt(db, db.Intents[0], "10.0.0.0/24", "", "", 0, true)

	for _, want := range []string{
		// The brief opens with the task source, not with an anonymous role line.
		// The testDB fixture has no CreatedBy (legacy row), so the source is
		// omitted and only the plain executor opening remains.
		expectedExecutorOpening,
		"意图标题、说明和验收标准用简体中文撰写，通过接口保存时同样适用",
		"命令和引用原文按原样保留",
		"## 本轮指派",
		"验证 /admin 越权",
		"普通用户能否读到全部订单",
		"用 uid=2 的 cookie 请求 GET /admin",
		"## 派生自这些事实",
		"（摘要层；正文与证据按 fact id 取）",
		"10.0.0.5 开放 8080",
		"tomcat 9.0.31",
		"## 简报",
		"已停放（明确不再走）",
		"测 SSRF",
		"需要内网出口",
		"上次失败（只是历史，是否重走自己判断）",
		"扫端口",
		"没有记录原因",
		"在跑的其他意图",
		"测登录限速",
		"10.0.0.0/24",
		// The boundary is a dynamic snapshot: assets come and go during a test
		// and the operator updates it through messages, never through a parser.
		// The snapshot is the only cross-intent view a run has (D58).
		"## 本轮输入文件（只读）",
		runInputFile,
		"不要把整份文件读进上下文",
		"## 交接（必做）",
		".tw/facts.jsonl",
		"跑完没结论也要写一条 `note`",
		// The evidence example must be a path the model can copy: the old
		// placeholder ("本目录真实文件") said nothing about where to put it, and
		// the false rule "`.tw/` 里的不算" contradicted the ledger (any real
		// file under the case counts, as long as the declared path matches).
		`"evidence":["api-users.json"]`,
		"证据放 case 根目录",
		"路径必须和文件实际位置一致",
		// Titles are what the graph and board show; host/service stay machine
		// keys, everything else is Chinese prose with the raw text in doc.
		"`title` 是索引",
		"简体中文短句",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt missing %q\n---\n%s", want, p)
		}
	}

	// The worker brief must not carry the operator-defined project goals (audit
	// F12.3): the operator owns them and the worker's target is its own intent +
	// Eval.
	for _, unwanted := range []string{"拿到管理员权限", "vault fact", "## 目标"} {
		if strings.Contains(p, unwanted) {
			t.Errorf("worker brief leaked a global goal (%q):\n%s", unwanted, p)
		}
	}
	// The project endpoint goal is the operator's; the brief states that fact and
	// keeps the worker's own target boundary (its own intent + Eval).
	if !strings.Contains(p, "项目终点目标由操作员定义") || !strings.Contains(p, "本轮的目标就是本意图及其「验收标准」") {
		t.Errorf("brief does not state the worker's own target boundary:\n%s", p)
	}

	// The brief must not prescribe a penetration method or name a specific
	// harness tool: the harness supplies tool guidance, the operator owns the
	// method constraints.
	for _, unwanted := range []string{"换入口、换工具", "neutron 模板"} {
		if strings.Contains(p, unwanted) {
			t.Errorf("prompt still prescribes method or names a harness tool (%q):\n%s", unwanted, p)
		}
	}

	// The long contract moved into the skill (D23): the prompt keeps only the
	// minimum that must survive a missing skill.
	for _, unwanted := range []string{
		"交接契约（必读）",
		"不要用 `.tw/` 里的文件当证据",
		"`.tw/`、`.cyber/` 里的不算",
		// The fact doc (commands, raw response) must not ride the brief: it is
		// read by fact id (docs/17 §3.1).
		"Apache-Coyote/1.1",
	} {
		if strings.Contains(p, unwanted) {
			t.Errorf("prompt still carries the long form: %q", unwanted)
		}
	}

	// A failed run is history, not a verdict: it must not be listed as parked.
	if strings.Contains(p, "已停放（明确不再走）：\n- 扫端口") {
		t.Error("failed intent listed as parked")
	}
	if strings.Contains(p, "上次执行失败") {
		t.Error("prompt invented a failure reason")
	}

	// The intent itself must never be excluded from the brief by budget.
	if strings.Contains(p, "已停放（明确不再走）：\n- 验证 /admin 越权") {
		t.Error("intent listed as its own exclusion")
	}
	if strings.Contains(p, "- 测登录限速\n- 测登录限速") {
		t.Error("running list duplicated")
	}

	// Without a snapshot there is no pointer: the brief must not promise a file
	// that was never written.
	noGraph := BuildPrompt(db, db.Intents[0], "", "", "", 0, false)
	if strings.Contains(noGraph, runInputFile) {
		t.Error("prompt points at a snapshot that was not written")
	}
}

func TestHandoffAllowsProgressiveDeclarations(t *testing.T) {
	var b strings.Builder
	writeHandoff(&b)
	for _, want := range []string{"运行中确认一条结论即可追加", "不必等本轮结束", "每行一个完整 JSON"} {
		if !strings.Contains(b.String(), want) {
			t.Fatalf("missing progressive contract: %s", want)
		}
	}
}

// The graph and the handoff declare evidence on one base: the run's own case
// directory. The old prompt taught the "../" shape for `.tw/graph.json` and then
// required every declared facts path to open "在 case 根下", so models copied
// the graph form into facts.jsonl and the ledger dropped whole fact lines.
// Reused cross-case evidence is legal; the boundary is the project cases
// directory, not the single case directory.
func TestBuildPromptEvidenceBaseIsUnified(t *testing.T) {
	db := testDB()
	p := BuildPrompt(db, db.Intents[0], "10.0.0.0/24", "", "", 0, true)

	for _, want := range []string{
		"`evidence` 路径以**本轮 case 目录**为基准",
		"本 run 写 `raw/x`",
		"复用别的 case 写 `../in_x/…`",
		"图和接口里显示的是 cases 根相对（带 `in_xxx/` 前缀）",
		"声明路径必须和文件实际位置一致",
		"解析结果必须落在项目 cases 目录内的某个 case",
		"`../../x` 会让整行 JSON 被跳过",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt missing the unified evidence rule %q\n---\n%s", want, p)
		}
	}

	for _, unwanted := range []string{
		// The old rule rejected the very "../in_x/…" form graph.json shows.
		"每个路径都能在 case 根下打开",
		"形如 `../in_x/file`",
	} {
		if strings.Contains(p, unwanted) {
			t.Errorf("prompt still carries the old contradicting rule %q\n---\n%s", unwanted, p)
		}
	}
}

// Evidence quality: the prompt must say what makes a file useful as proof, not
// only where to put it. The report copies declared evidence verbatim, so a
// script or a model-written note declared as evidence reaches the client as
// "proof" while proving nothing (D94).
func TestBuildPromptDefinesUsefulEvidence(t *testing.T) {
	db := testDB()
	p := BuildPrompt(db, db.Intents[0], "10.0.0.0/24", "", "", 0, false)

	for _, want := range []string{
		"## 证据（原始材料）",
		"目标、请求/命令、响应/输出原文",
		"脚本、说明、中间产物不算证据",
		"它们跑出来的原始输出才算",
		"每条结论附可复核的原始证据",
		"只登记为线索（confidence=inferred）",
		"不写成已验证结论",
	} {
		if !strings.Contains(p, want) {
			t.Fatalf("prompt missing evidence standard %q:\n%s", want, p)
		}
	}

	// The standard belongs to the deliverable, stated before the declaration
	// mechanics: read after the handoff header, it is just an afterthought.
	if strings.Index(p, "## 证据") > strings.Index(p, "## 交接") {
		t.Fatalf("evidence contract placed after handoff:\n%s", p)
	}
}

func TestEvidenceContractRemainsWithoutProjectConstraints(t *testing.T) {
	db := testDB()
	p := BuildPrompt(db, db.Intents[0], "", "", "", 0, false)
	if !strings.Contains(p, "每条结论附可复核的原始证据") || !strings.Contains(p, "不写成已验证结论") {
		t.Fatal("disabling project constraints removed the executor evidence contract")
	}
}

// A retry note is decider commentary for humans, not an intent constraint. It
// lives in Intent.Notes, which BuildPrompt never reads; the failure line uses
// structured state, never the harness exit log, so evaluator internals
// ("verdict") cannot leak through history either.
func TestBuildPromptSeparatesRetryNotesAndHarnessLog(t *testing.T) {
	db := testDB()
	db.Intents[0].Doc = "原约束：只读枚举。"
	db.Intents[0].Notes = []model.IntentNote{{
		At: time.Now(), Kind: model.IntentNoteRetry,
		Text: "上次评测器调用错误导致 eval_error，并调用 verdict 落库。",
	}}
	db.Intents[0].RunID = "r_prev"
	db.Intents[0].Attempts = 2
	db.Runs = []model.Run{{
		ID: "r_prev", IntentID: db.Intents[0].ID, Status: model.RunFailed,
		StopReason: "completed", LastState: "eval_error",
		ExitReason: "evaluate attempt 1 failed: model did not call verdict tool",
	}}

	p := BuildPrompt(db, db.Intents[0], "10.0.0.0/24", "", "", 0, false)

	for _, unwanted := range []string{"verdict", "评测器", "重派说明"} {
		if strings.Contains(strings.ToLower(p), strings.ToLower(unwanted)) {
			t.Fatalf("prompt still carries decider/evaluator text %q:\n%s", unwanted, p)
		}
	}
	if !strings.Contains(p, "原约束：只读枚举") {
		t.Fatalf("prompt lost the real constraint:\n%s", p)
	}
	if !strings.Contains(p, "上次结束：eval_error") {
		t.Fatalf("prompt did not use the structured run state:\n%s", p)
	}
}

// A stray `n in a Go string literal looks like a newline and is not one; it
// reaches the executor as literal backtick-n. Cheap to check, silent when
// broken. Backticked words (`note`, `neutron`) are fine — only a `n that is
// not followed by a letter is a broken escape.
// Tool use belongs to the harness, not to our dispatch prompt. The executor
// already gets the harness's own tool descriptions and shell; writing shell or
// file-tool instructions here contradicted them (the old cmd.exe line made the
// model wrap commands in another shell). Keep only the task and handoff
// contract.
func TestBuildPromptLeavesToolUseToTheHarness(t *testing.T) {
	db := testDB()
	p := BuildPrompt(db, db.Intents[0], "10.0.0.0/24", "/data/pocs", "/data/kb", 0, false)
	low := strings.ToLower(p)

	for _, unwanted := range []string{
		"bash",
		"cmd.exe",
		"powershell",
		"shell",
		"grep",
		"glob",
		"read/",
		"subagent",
		"/dev/null",
		"curl -o",
		"neutron -u",
		"neutron -i",
		// The POC library pointer must stay vendor-neutral: the executor finds
		// templates from the case path, not from a named harness tool.
		"neutron 模板",
	} {
		if strings.Contains(low, unwanted) {
			t.Errorf("prompt still teaches tool use: %q", unwanted)
		}
	}

	// The task contract must survive the cleanup.
	for _, want := range []string{
		"## 纪律",
		"## 交接（必做）",
		"证据放 case 根目录",
		"写进 `.tw/facts.jsonl`",
		"项目打法库",
		"是否记录和复用由你判断",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt lost task contract %q", want)
		}
	}
}

// toolCommandLine matches a harness tool name followed by a flag — the shape of
// an actual invocation, not a bare tool or directory name. The harness's own
// tool descriptions are already in the model's context, so a command line
// restated in the prompt is a second truth: update one side and the model reads
// the stale copy.
var toolCommandLinePattern = regexp.MustCompile(`(?i)\b(neutron|gogo|katana|proton|spray|zombie|terminal|curl|nuclei)\s+-{1,2}[a-zA-Z]`)

// TestPromptCarriesNoToolCommandLines pins the boundary between what the control
// plane owns and what the harness owns: a prompt assigns which library or file
// belongs to this round, which endpoint and request header reach the battlefield,
// and what counts as done; how a harness tool is invoked — curl included —
// belongs to that tool's description. It covers the dispatch brief and the
// commander round prompt.
func TestPromptCarriesNoToolCommandLines(t *testing.T) {
	db := testDB()
	in := db.Intents[0]
	variants := []struct {
		name   string
		prompt string
	}{
		{"full dispatch", BuildPromptWithInstructions(db, in, "10.0.0.0/24", "/data/pocs", "/data/kb",
			time.Minute, true, true, "上一轮接班笔记：先核 /admin 的读权限。", "\n## 当前持久指令\n- 只读。\n", "")},
		{"no optional summaries", buildPromptWithoutOptionalSummaries(db, in, "10.0.0.0/24", "/data/pocs", "/data/kb",
			time.Minute, true, true, "\n## 当前持久指令\n- 只读。\n", "", false)},
		{"bare dispatch", BuildPrompt(db, in, "", "/data/pocs", "", 0, false)},
		{"commander turn input", (&Server{APIBase: "http://127.0.0.1:8080"}).CommanderHeader() + "\n\n" +
			decider.CommanderRoundPrompt(board.View{}, "## 笔记正文\n固定夹具。\n",
				"10.0.0.0/24", decider.ContextStatus{}, decider.RoundContext{})},
	}
	for _, v := range variants {
		loc := toolCommandLinePattern.FindStringIndex(v.prompt)
		if loc == nil {
			continue
		}
		lineNo, line := promptLineAt(v.prompt, loc[0])
		t.Fatalf("%s: prompt restates a harness tool command line %q at line %d (byte %d); the tool description already covers it:\n%s",
			v.name, v.prompt[loc[0]:loc[1]], lineNo, loc[0], line)
	}
}

// promptLineAt returns the 1-based line number of byte offset at and that line's
// text, so a gate failure points at the exact segment to remove.
func promptLineAt(p string, at int) (int, string) {
	start := strings.LastIndex(p[:at], "\n") + 1
	end := strings.Index(p[at:], "\n")
	if end < 0 {
		end = len(p)
	} else {
		end += at
	}
	return strings.Count(p[:at], "\n") + 1, p[start:end]
}

// The POC section assigns this round's library and nothing more. How to point
// the scanner at it is the harness tool description's contract; restating the
// flag and its default here duplicated that contract and could drift from it.
func TestPOCSectionAssignsLibraryWithoutToolUsage(t *testing.T) {
	db := testDB()
	p := BuildPrompt(db, db.Intents[0], "", "/data/pocs", "", 0, false)
	for _, want := range []string{"本轮 POC 库目录：/data/pocs", "本轮的模板集就是它"} {
		if !strings.Contains(p, want) {
			t.Fatalf("prompt lost POC assignment %q:\n%s", want, p)
		}
	}
	start := strings.Index(p, "## POC 库")
	if start < 0 {
		t.Fatalf("prompt lost the POC section:\n%s", p)
	}
	section := p[start:]
	if end := strings.Index(section, "\n## "); end >= 0 {
		section = section[:end]
	}
	for _, unwanted := range []string{"neutron", "-t", "-j", "用法"} {
		if strings.Contains(section, unwanted) {
			t.Fatalf("POC section restates tool usage %q:\n%s", unwanted, section)
		}
	}
}

func TestBuildPromptHasNoStrayBacktickN(t *testing.T) {
	db := testDB()
	p := BuildPrompt(db, db.Intents[0], "10.0.0.0/24", "/data/pocs", "/data/kb", time.Minute, true)
	for i := 0; ; {
		j := strings.Index(p[i:], "`n")
		if j < 0 {
			break
		}
		at := i + j
		next := byte(0)
		if at+2 < len(p) {
			next = p[at+2]
		}
		if next == 0 || !((next >= 'a' && next <= 'z') || (next >= 'A' && next <= 'Z')) {
			t.Fatalf("prompt contains a literal `n instead of a newline at %d:\n%s", at, p)
		}
		i = at + 2
	}
}

func TestCommanderDocsCarryConstraintAndStart(t *testing.T) {
	s := New(nil, nil, t.TempDir(), t.TempDir())
	s.Scope = "10.0.0.0/24"
	s.Description = "不要动平台的数据"
	if got := s.Constraints(); got != "不要动平台的数据" {
		t.Fatalf("constraints = %q, want the document verbatim", got)
	}
	got := s.CommanderDocs()
	for _, want := range []string{"## 约束\n不要动平台的数据", "## 起点\n10.0.0.0/24"} {
		if !strings.Contains(got, want) {
			t.Fatalf("commander docs missing %q: %s", want, got)
		}
	}
	if strings.Contains(got, "授权") {
		t.Fatalf("commander docs must not talk about authorization: %s", got)
	}
}

func TestBuildPromptCarriesEvalAndPlaybooks(t *testing.T) {
	db := store.DB{
		Intents: []model.Intent{{
			ID: "in_x", Title: "复用", Description: "d",
			Eval:        "至少找到一条可复现的凭据复用路径",
			FromFactIDs: []string{"f_note"},
		}},
		Facts: []model.Fact{
			{ID: "f_note", Kind: "note", Title: "普通事实", Summary: "不是打法"},
			// A confirmed playbook needs sources that a verifier proved and that
			// carry bytes; otherwise the brief labels it a candidate.
			{ID: "f_src", Kind: "vuln", Title: "已核验来源", Status: "verified", EvidenceIDs: []string{"ev_1"}},
		},
		Playbooks: []model.Playbook{
			{ID: "pb_old", Title: "旧打法", When: "旧", Status: model.PlaybookConfirmed, Origin: model.PlaybookFromExecutor, FromFactIDs: []string{"f_src"}},
			{ID: "pb_1", Title: "ThinkPHP RCE", When: "5.0.x 可用", Doc: "curl /index.php?s=...",
				Status: model.PlaybookConfirmed, Origin: model.PlaybookFromExecutor, Outcome: model.PlaybookSuccess, FromFactIDs: []string{"f_src"}},
		},
	}
	p := BuildPrompt(db, db.Intents[0], "", "", "/data/kb", 0, false)
	for _, want := range []string{"## 验收标准", "可复现的凭据复用", "## 可复用打法", "[确认] ThinkPHP RCE", "curl /index.php", ".tw/kb/entries", "已复制到本轮 case", "### f_note · note"} {
		if !strings.Contains(p, want) {
			t.Fatalf("prompt missing %q:\n%s", want, p)
		}
	}
	// A playbook is knowledge, not a fact: it must not leak into the fact
	// sections, and the prompt must say it needs checking.
	if !strings.Contains(p, "用之前先按现场核对") {
		t.Fatalf("playbook section lost its untrusted-data warning:\n%s", p)
	}
	// No posture section: division of labor comes from the direction itself,
	// not from a role the commander labels the executor with (D65).
	if strings.Contains(p, "## 角色") {
		t.Fatalf("executor prompt still carries a role section:\n%s", p)
	}
}

func TestPlaybookRecallKeepsRelevantAndOmitsUnrelatedEntries(t *testing.T) {
	now := time.Now()
	db := store.DB{
		Intents: []model.Intent{{
			ID: "in_x", Title: "打 ThinkPHP", Description: "thinkphp 5.0 的站",
		}},
		Playbooks: []model.Playbook{
			{ID: "pb_recent", Title: "Redis 未授权", When: "6379", Status: model.PlaybookConfirmed, UpdatedAt: now},
			{ID: "pb_tag", Title: "ThinkPHP RCE", When: "5.0.x 可用",
				Tags: []string{"product:thinkphp", "technique:rce"}, Status: model.PlaybookConfirmed, UpdatedAt: now.Add(-time.Hour)},
		},
	}
	p := BuildPrompt(db, db.Intents[0], "", "", "", 0, false)
	relevant := strings.Index(p, "ThinkPHP RCE")
	recent := strings.Index(p, "Redis 未授权")
	if relevant < 0 || recent >= 0 {
		t.Fatalf("relevant playbook not first (relevant=%d recent=%d):\n%s", relevant, recent, p)
	}
}

// Only three confirmed entries fit in the brief, and the slots go to entries
// whose sources actually back them: a writer's declaration with an unverified
// source is a candidate line, never a fourth "[确认]" and never a displaced
// qualified entry.
func TestUnverifiedDeclarationDoesNotTakeAConfirmedSlot(t *testing.T) {
	qualified := func(id, title string) model.Playbook {
		return model.Playbook{ID: id, Title: title, When: "thinkphp 5.0", Status: model.PlaybookConfirmed,
			FromFactIDs: []string{"f_good"}}
	}
	db := store.DB{
		Intents: []model.Intent{{ID: "in_x", Title: "打 ThinkPHP", Description: "thinkphp 5.0"}},
		Facts: []model.Fact{
			{ID: "f_good", Kind: "vuln", Title: "已核验来源", Status: "verified", EvidenceIDs: []string{"ev_1"}},
			{ID: "f_bad", Kind: "vuln", Title: "未核验来源"},
		},
		Playbooks: []model.Playbook{
			qualified("pb_a", "ThinkPHP 打法 A"),
			qualified("pb_b", "ThinkPHP 打法 B"),
			qualified("pb_c", "ThinkPHP 打法 C"),
			{ID: "pb_bad", Title: "ThinkPHP 未核验打法", When: "thinkphp 5.0",
				Status: model.PlaybookConfirmed, FromFactIDs: []string{"f_bad"}},
		},
	}
	p := BuildPrompt(db, db.Intents[0], "", "", "", 0, false)
	for _, want := range []string{"[确认] ThinkPHP 打法 A", "[确认] ThinkPHP 打法 B", "[确认] ThinkPHP 打法 C"} {
		if !strings.Contains(p, want) {
			t.Fatalf("qualified entry missing %q:\n%s", want, p)
		}
	}
	if got := strings.Count(p, "[确认]"); got != 3 {
		t.Fatalf("confirmed lines = %d, want the 3 qualified entries:\n%s", got, p)
	}
	if !strings.Contains(p, "[候选·未验证] ThinkPHP 未核验打法") {
		t.Fatalf("unqualified declaration is not a candidate line:\n%s", p)
	}
	if strings.Contains(p, "[确认] ThinkPHP 未核验打法") {
		t.Fatalf("an unverified source was labelled confirmed:\n%s", p)
	}
}

func TestNegativePlaybooksRemainHistoryWithoutAutomaticReuse(t *testing.T) {
	db := store.DB{Intents: []model.Intent{{ID: "in_x", Title: "current task"}}, Playbooks: []model.Playbook{
		{ID: "pb_negative", Title: "historical-negative-marker", Outcome: model.PlaybookDeadEnd, Status: model.PlaybookConfirmed},
		{ID: "pb_positive", Title: "reusable-success-marker", When: "current task", Outcome: model.PlaybookSuccess, Status: model.PlaybookConfirmed},
	}}
	p := BuildPrompt(db, db.Intents[0], "", "", "", 0, false)
	if strings.Contains(p, "historical-negative-marker") || !strings.Contains(p, "reusable-success-marker") {
		t.Fatalf("recall=%s", p)
	}
	if len(db.Playbooks) != 2 {
		t.Fatal("history was changed")
	}
}

// A commander's candidate is a lead, not knowledge: it appears only when it
// matches the direction, it is labelled, and it never displaces the confirmed
// entry it may be about.
func TestPlaybookCandidatesAreLabelledAndRankedBelowConfirmed(t *testing.T) {
	db := store.DB{
		Intents: []model.Intent{{ID: "in_x", Title: "打 ThinkPHP", Description: "thinkphp 5.0"}},
		Facts: []model.Fact{
			{ID: "f_src", Kind: "vuln", Title: "已核验来源", Status: "verified", EvidenceIDs: []string{"ev_1"}},
		},
		Playbooks: []model.Playbook{
			{ID: "pb_confirmed", Title: "ThinkPHP RCE", When: "5.0.x 可用", Status: model.PlaybookConfirmed, FromFactIDs: []string{"f_src"}},
			{ID: "pb_candidate", Title: "ThinkPHP 反序列化", When: "thinkphp 版本上探到 5.1 再试",
				Tags:   []string{"product:thinkphp"},
				Status: model.PlaybookCandidate, Origin: model.PlaybookFromCommander},
			{ID: "pb_unrelated", Title: "Redis 未授权", When: "6379", Status: model.PlaybookCandidate, Origin: model.PlaybookFromCommander},
			{ID: "pb_dead", Title: "旧入口", When: "thinkphp 已修", Status: model.PlaybookRetired},
		},
	}
	p := BuildPrompt(db, db.Intents[0], "", "", "", 0, false)
	if !strings.Contains(p, "[候选·未验证] ThinkPHP 反序列化") {
		t.Fatalf("candidate missing or unlabelled:\n%s", p)
	}
	if strings.Contains(p, "Redis 未授权") {
		t.Fatalf("an unrelated candidate took a slot:\n%s", p)
	}
	if strings.Contains(p, "旧入口") {
		t.Fatalf("retired playbook still recalled:\n%s", p)
	}
	if strings.Index(p, "ThinkPHP RCE") > strings.Index(p, "ThinkPHP 反序列化") {
		t.Fatalf("confirmed entry must come first:\n%s", p)
	}
}

func TestBuildPromptCarriesOwnLastFailure(t *testing.T) {
	db := store.DB{
		Intents: []model.Intent{{
			ID: "in_x", Title: "重试", Description: "再试一次",
			RunID: "r_old", Attempts: 2,
		}},
		Runs: []model.Run{{
			ID: "r_old", IntentID: "in_x", Status: model.RunFailed,
			StopReason: "provider_auth", DeclaredFacts: 1,
		}},
	}
	p := BuildPrompt(db, db.Intents[0], "", "", "", 0, false)
	for _, want := range []string{"## 本意图历史", "第 2 次派发", "provider_auth", "交接事实 1 条"} {
		if !strings.Contains(p, want) {
			t.Fatalf("prompt missing %q:\n%s", want, p)
		}
	}
	// The history section is a record, not advice: the old line prescribed
	// switching entry/tool/evidence on retry, which is the model's own call.
	if !strings.Contains(p, "以上只是历史记录，不代表本轮必须怎么做") {
		t.Fatalf("history section does not mark itself as non-prescriptive:\n%s", p)
	}
	if strings.Contains(p, "换入口、换工具") {
		t.Fatalf("history section prescribes the next attempt:\n%s", p)
	}
}

// The worker must not receive the operator-defined project goals (audit F12.3):
// the operator owns them and the worker's target is its own intent + Eval.
// This is a role boundary, not a size optimisation — a long prompt used to emit
// only the first goal, and the old test pinned "every goal survives"; now no
// goal survives, on purpose.
func TestBuildPromptDoesNotInjectGlobalGoals(t *testing.T) {
	var long strings.Builder
	for i := 0; i < 800; i++ {
		long.WriteString("细节")
	}
	db := store.DB{
		Intents: []model.Intent{{
			ID: "in_x", Title: "多目标", Description: "x",
			Doc:  long.String(),
			Eval: "本意图自己的验收标准",
		}},
		Goals: []model.Goal{
			{ID: "g_1", Statement: "目标一"},
			{ID: "g_2", Statement: "目标二"},
			{ID: "g_3", Statement: "目标三"},
		},
	}
	p := BuildPrompt(db, db.Intents[0], "", "", "", 0, false)
	for _, banned := range []string{"目标一", "目标二", "目标三"} {
		if strings.Contains(p, banned) {
			t.Fatalf("worker brief carried global goal %q:\n%s", banned, p)
		}
	}
	// The worker's own target and acceptance criterion must still be there.
	for _, want := range []string{"本意图自己的验收标准", "项目终点目标由操作员定义", "多个意图之间的取舍由指挥官掌握", "本轮的目标就是本意图及其「验收标准」"} {
		if !strings.Contains(p, want) {
			t.Fatalf("prompt missing %q:\n%s", want, p)
		}
	}
}

func TestBuildPromptTruncatesSourceFacts(t *testing.T) {
	big := strings.Repeat("细节", 400) // over the per-fact summary budget
	var facts []model.Fact
	var ids []string
	for i := 0; i < 40; i++ {
		id := "f_" + string(rune('a'+i%26)) + string(rune('a'+i/26))
		ids = append(ids, id)
		facts = append(facts, model.Fact{ID: id, Kind: "host", Status: "verified", Title: "事实 " + id, Summary: big})
	}
	db := store.DB{
		Intents: []model.Intent{{ID: "in_x", Title: "大清单", FromFactIDs: ids}},
		Facts:   facts,
	}
	p := BuildPrompt(db, db.Intents[0], "", "", "", 0, false)
	if !strings.Contains(p, "（已截断，其余事实未列出；需要时按 fact id 取）") {
		t.Fatalf("expected truncation marker, got:\n%s", p)
	}
	if n := len([]rune(p)); n > factsBudget+5000 {
		t.Fatalf("prompt not bounded: %d runes", n)
	}
}

// The dispatch brief carries the summary layer: id/kind/status/title, a clipped
// summary and the evidence state. Commands and raw output (the doc) stay in the
// ledger and are read by id (docs/17 §3.1).
func TestBuildPromptFactRowsAreTheSummaryLayer(t *testing.T) {
	summary := strings.Repeat("可复现的注入点。", 100) // 800 runes
	db := store.DB{
		Intents: []model.Intent{{ID: "in_x", Title: "看一条事实", FromFactIDs: []string{"f_1"}}},
		Facts: []model.Fact{{
			ID: "f_1", Kind: "vuln", Title: "SQL 注入", Status: "verified",
			Summary:     summary,
			Doc:         "curl -s 'http://t/?id=1' --data ...\n原始响应正文",
			EvidenceIDs: []string{"e_1"},
		}},
	}
	p := BuildPrompt(db, db.Intents[0], "", "", "", 0, false)

	for _, want := range []string{
		"### f_1 · vuln · verified · SQL 注入",
		"可复现的注入点。",
		"证据：1 条",
		"（摘要层；正文与证据按 fact id 取）",
		"…",
	} {
		if !strings.Contains(p, want) {
			t.Fatalf("prompt missing %q:\n%s", want, p)
		}
	}
	for _, bad := range []string{"curl -s", "原始响应正文", summary} {
		if strings.Contains(p, bad) {
			t.Fatalf("prompt carries the doc body or the full summary (%q):\n%s", bad, p)
		}
	}
}

// Knowledge that does not speak to this direction must not open an empty
// section: a header with nothing under it costs tokens and teaches nothing.
func TestNoPlaybookSectionWhenNothingMatches(t *testing.T) {
	db := store.DB{
		Intents: []model.Intent{{ID: "in_x", Title: "打 SSH", Description: "22 端口"}},
		Playbooks: []model.Playbook{
			{ID: "pb_1", Title: "Redis 未授权", When: "6379", Status: model.PlaybookCandidate},
			{ID: "pb_2", Title: "老打法", When: "x", Status: model.PlaybookRetired},
		},
	}
	p := BuildPrompt(db, db.Intents[0], "", "", "", 0, false)
	if strings.Contains(p, "## 可复用打法") {
		t.Fatalf("empty playbook section must not be emitted:\n%s", p)
	}
}

// A fresh dispatch of a recycled direction must carry the note the previous
// run left behind: the new session has no conversation memory at all.
func TestBuildPromptRecordsRecoveredWorkerNoteWithoutBody(t *testing.T) {
	db := testDB()
	note := "已确认：/admin 对普通用户返回 200\n下一步：导出订单并留原始响应"
	p := BuildPromptWithWorkerNote(db, db.Intents[0], "", "", "", 0, false, note)

	for _, want := range []string{
		"## 你的笔记（worker-note.md，来自上一次运行）",
		"正文由操作员审阅",
		// The recycling contract is always there for a worker, note or not.
		"## 上下文与回收",
		"`.tw/worker-note.md`",
		// The shared battlefield is readable with the round's own token.
		"战场态势（只读）",
	} {
		if !strings.Contains(p, want) {
			t.Fatalf("prompt missing %q:\n%s", want, p)
		}
	}
	if strings.Contains(p, note) {
		t.Fatal("worker note prose entered the dispatch prompt")
	}
	if !strings.Contains(p, "harness 会在上下文接近上限时自动压缩") {
		t.Fatalf("recycling section must say compaction is automatic:\n%s", p)
	}
}

// The worker note contract is soft and about role, like the commander's: no
// line/byte quota, facts cited by id instead of copied, and the length stays the
// worker's judgement. A hard cap would turn the note into a form to fill and
// would silently cut the tail where the next step lives (D106).
func TestWorkerPromptCarriesTheSoftNoteContract(t *testing.T) {
	db := testDB()
	p := BuildPrompt(db, db.Intents[0], "", "", "", 0, false)
	for _, want := range []string{"供操作员查看的历史笔记", "事实和证据按 ID 引用"} {
		if !strings.Contains(p, want) {
			t.Fatalf("worker prompt missing %q:\n%s", want, p)
		}
	}
	for _, banned := range []string{"8KB", "笔记预算"} {
		if strings.Contains(p, banned) {
			t.Fatalf("worker prompt states a hard note quota (%q):\n%s", banned, p)
		}
	}
}

// A first dispatch has no note to recover: the section is absent, not empty.
func TestBuildPromptWithoutWorkerNoteHasNoNoteSection(t *testing.T) {
	db := testDB()
	p := BuildPrompt(db, db.Intents[0], "", "", "", 0, false)
	if strings.Contains(p, "## 你的笔记") {
		t.Fatalf("first dispatch carried a recovered-note section:\n%s", p)
	}
	if !strings.Contains(p, "## 上下文与回收") {
		t.Fatalf("first dispatch must still state the context/recycling contract:\n%s", p)
	}
}

// The graph snapshot is a bounded index, not a document to paste into the turn.
// A prompt that says "read it first" makes every short-lived run pay for the
// whole file even when only one fact matters; the cost grows with the campaign.
func TestGraphPointerSpendsContextOnDemand(t *testing.T) {
	db := testDB()
	p := BuildPrompt(db, db.Intents[0], "", "", "", 0, true)
	if !strings.Contains(p, "不要把整份文件读进上下文") {
		t.Fatalf("graph pointer does not warn against reading the whole snapshot:\n%s", p)
	}
	if !strings.Contains(p, "事实按 ID 查 `GET /api/facts/<id>`") {
		t.Fatalf("graph pointer does not say how to read details:\n%s", p)
	}
	if strings.Contains(p, "先看它：") {
		t.Fatalf("old whole-file instruction is still present:\n%s", p)
	}
}

// TestTaskSourceLine pins the opening "task source" line. The first line says
// where the direction came from — commander dispatch, a peer executor's parallel
// publication, or the operator — and falls back to the plain executor opening
// when CreatedBy is empty/unclassifiable (old rows). It is attribution, not
// permission: the authorization boundary and the persistent instructions still
// follow, and no role may be claimed in the first person.
func TestTaskSourceLine(t *testing.T) {
	const (
		commanderExtra    = "该意图由指挥官基于全局前沿判定下发。"
		peerExtra         = "你与发起者是共享账本的同级执行者。你独立核对证据，并对本轮结论负责。"
		authorizationNote = "授权来源"
	)
	const sourceTitle = "发现 /admin 的独立线索"

	cases := []struct {
		name      string
		createdBy string
		// sourceTag is the expected bracketed source tag; empty means the
		// legacy fallback opening with no tag at all.
		sourceTag string
		extra     string
		// titled is true when the source direction's title must appear inside
		// the tag; untitled is true when the tag must omit the 「title」 part.
		titled   bool
		untitled bool
	}{
		{name: "commander", createdBy: collab.CommanderID, sourceTag: "【任务来源：指挥官全局派发】", extra: commanderExtra},
		{name: "peer executor", createdBy: "pt_in_src", sourceTag: "【任务来源：同僚执行者「" + sourceTitle + "」平级发布】", extra: peerExtra, titled: true},
		{name: "peer source missing from ledger", createdBy: "pt_in_missing", sourceTag: "【任务来源：同僚执行者平级发布】", extra: peerExtra, untitled: true},
		{name: "peer source blank title", createdBy: "pt_in_blank", sourceTag: "【任务来源：同僚执行者平级发布】", extra: peerExtra, untitled: true},
		{name: "operator", createdBy: collab.OperatorID, sourceTag: "【任务来源：操作员】"},
		// Old rows carry no CreatedBy, and a non-collab string is not an
		// initiator the ledger can resolve: the prompt names no source.
		{name: "legacy empty", createdBy: ""},
		{name: "legacy unknown participant", createdBy: "pt"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := testDB()
			db.Intents = append(db.Intents,
				model.Intent{ID: "in_src", Title: sourceTitle},
				model.Intent{ID: "in_blank", Title: " \n\t "},
			)
			in := model.Intent{ID: "in_target", Title: "验证订单越权", CreatedBy: tc.createdBy}
			p := BuildPrompt(db, in, "10.0.0.0/24", "", "", time.Minute, false)

			lines := strings.SplitN(p, "\n", 3)
			if len(lines) < 2 || lines[0] == "" {
				t.Fatalf("prompt has no opening line:\n%s", p)
			}
			// The operator-priority paragraph comes first; the task-source line and
			// the current time follow it.
			if strings.TrimSpace(lines[0]) != strings.TrimSpace(contract.OperatorPriority) {
				t.Fatalf("first paragraph = %q, want the operator priority sentence", lines[0])
			}
			first := lines[1]
			// Everything after the source line is one block: 约束 and the
			// assignment come before the current-time line.
			if rest := lines[2]; strings.Index(rest, "当前时间：") < 0 ||
				strings.Index(rest, "## 约束") > strings.Index(rest, "当前时间：") {
				t.Fatalf("constraint/time order changed after the source line:\n%s", p)
			}
			if !strings.Contains(first, expectedExecutorOpening) {
				t.Fatalf("opening line lost the executor task statement: %q", first)
			}
			if tc.sourceTag == "" {
				if first != expectedExecutorOpening {
					t.Fatalf("legacy opening = %q, want exactly %q", first, expectedExecutorOpening)
				}
				if strings.Contains(first, "【任务来源") {
					t.Fatalf("legacy row invented a task source: %q", first)
				}
			} else {
				if !strings.HasPrefix(first, tc.sourceTag) {
					t.Fatalf("opening = %q, want source tag prefix %q", first, tc.sourceTag)
				}
				if n := strings.Count(first, "【任务来源："); n != 1 {
					t.Fatalf("opening carries %d task source tags, want 1: %q", n, first)
				}
			}
			if tc.extra != "" && !strings.Contains(first, tc.extra) {
				t.Fatalf("opening line missing the source-specific sentence %q: %q", tc.extra, first)
			}
			if tc.titled && !strings.Contains(first, "「"+sourceTitle+"」") {
				t.Fatalf("opening line missing the source direction title: %q", first)
			}
			if tc.untitled && (strings.Contains(first, "「") || strings.Contains(first, "」")) {
				t.Fatalf("opening line must omit the unknown title, not leave empty quotes: %q", first)
			}
			if strings.Contains(p, authorizationNote) {
				t.Fatalf("prompt still explains that the task source is not permission:\n%s", p)
			}
			// No first-person role claims anywhere in the prompt.
			for _, banned := range []string{"我是执行者", "我是指挥官", "我是操作员"} {
				if strings.Contains(p, banned) {
					t.Fatalf("prompt claims a role in the first person: %q", banned)
				}
			}
			// Order: opening → 约束 → 本轮指派 → current time → hard timeout.
			at := func(marker string) int { return strings.Index(p, marker) }
			opening, constraints, assignment := at(expectedExecutorOpening), at("## 约束"), at("## 本轮指派")
			now, hardTimeout := at("当前时间："), at("## 硬超时")
			if opening < 0 || constraints < 0 || assignment < 0 || now < 0 || hardTimeout < 0 {
				t.Fatalf("prompt section missing:\n%s", p)
			}
			if !(opening < constraints && constraints < assignment && assignment < now && now < hardTimeout) {
				t.Fatalf("section order: opening=%d constraints=%d assignment=%d time=%d timeout=%d",
					opening, constraints, assignment, now, hardTimeout)
			}
		})
	}
}

// TestTaskSourceTitleFoldedAndClipped pins the source-title treatment: old
// titles may carry newlines, and a title is only a label, so it is folded to one
// line and clipped at 40 runes. An empty title omits the「title」part instead of
// producing empty quotes.
func TestTaskSourceTitleFoldedAndClipped(t *testing.T) {
	const tail = "长"
	db := testDB()
	db.Intents = append(db.Intents,
		model.Intent{ID: "in_long", Title: "第一行\n  第二行 " + strings.Repeat(tail, 60)},
		model.Intent{ID: "in_blank", Title: " \n\t "},
	)

	firstLine := func(sourceID string) string {
		t.Helper()
		in := model.Intent{ID: "in_target", Title: "目标", CreatedBy: "pt_" + sourceID}
		p := BuildPrompt(db, in, "", "", "", 0, false)
		// The task-source line is the role/identity paragraph; the operator
		// priority sentence precedes it now, so the block is located by its
		// marker instead of by line number.
		for _, line := range strings.Split(p, "\n") {
			if strings.Contains(line, "【任务来源") || line == expectedExecutorOpening {
				return line
			}
		}
		t.Fatalf("prompt lost the task-source line:\n%s", p)
		return ""
	}

	first := firstLine("in_long")
	start := strings.Index(first, "「")
	end := strings.Index(first, "」")
	if start < 0 || end < start {
		t.Fatalf("source title missing from the opening line: %q", first)
	}
	title := first[start+len("「") : end]
	if strings.Contains(title, "\n") || strings.Contains(title, "\r") {
		t.Fatalf("source title was not folded to one line: %q", title)
	}
	if !strings.HasPrefix(title, "第一行 第二行 长") {
		t.Fatalf("folded title lost its text: %q", title)
	}
	if !strings.HasSuffix(title, "…") {
		t.Fatalf("over-long title was not clipped: %q", title)
	}
	r := []rune(title)
	if len(r) != taskSourceTitleRunes+1 {
		t.Fatalf("clipped title has %d runes, want %d runes + ellipsis", len(r), taskSourceTitleRunes)
	}
	if strings.Contains(title, strings.Repeat(tail, 60)) {
		t.Fatalf("clipped title still carries the whole source title: %q", title)
	}

	blank := firstLine("in_blank")
	if strings.Contains(blank, "「") || strings.Contains(blank, "」") {
		t.Fatalf("blank source title left empty quotes: %q", blank)
	}
	if !strings.HasPrefix(blank, "【任务来源：同僚执行者平级发布】") {
		t.Fatalf("blank source title lost the peer source tag: %q", blank)
	}
}

// TestFactStatusMarkers pins the one-line status label of a fact row. The old
// blanket "待复核假设" label treated a note, a rejected fact and an unverified
// fact as the same thing; each now says what it may be used for.
func TestFactStatusMarkers(t *testing.T) {
	cases := []struct {
		name   string
		fact   model.Fact
		marker string
	}{
		{"verified", model.Fact{ID: "f_ok", Kind: "host", Status: "verified", Title: "已核验"}, ""},
		{"note", model.Fact{ID: "f_note", Kind: "note", Status: "unverified", Title: "线索"},
			"【未核验线索：note 永不复核，不能当作前提；只能用于验证或反证】\n"},
		{"note with legacy status", model.Fact{ID: "f_note2", Kind: "note", Title: "旧线索"},
			"【未核验线索：note 永不复核，不能当作前提；只能用于验证或反证】\n"},
		{"rejected", model.Fact{ID: "f_rej", Kind: "vuln", Status: "rejected", Title: "已否定"},
			"【已否定：不能作为前提】\n"},
		{"withdrawn", model.Fact{ID: "f_wd", Kind: "note", Status: "withdrawn", Title: "已撤回"},
			"【已撤回：不构成结论，不得作为前提；仅可按 id 查阅历史】\n"},
		{"unverified", model.Fact{ID: "f_unv", Kind: "host", Status: "unverified", Title: "未核验"},
			"【未核验·待复核：不是已确认前提；只能用于验证或反证，不能代替 from_fact_ids 的准入】\n"},
		{"empty status", model.Fact{ID: "f_empty", Kind: "host", Title: "旧数据"},
			"【状态未知：按未核验对待】\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := renderFact(tc.fact, false)
			if tc.marker == "" {
				if strings.HasPrefix(got, "【") {
					t.Fatalf("verified fact carries a status label: %q", got)
				}
				return
			}
			if !strings.HasPrefix(got, tc.marker) {
				t.Fatalf("status label = %q, want prefix %q", got, tc.marker)
			}
		})
	}

	// Integration: source-facts rows use the same renderer, and the old blanket
	// label is gone from the prompt entirely.
	db := testDB()
	db.Intents[0].FromFactIDs = []string{"f_note", "f_rej", "f_unv", "f_empty"}
	db.Facts = []model.Fact{
		{ID: "f_note", Kind: "note", Status: "unverified", Title: "线索"},
		{ID: "f_rej", Kind: "vuln", Status: "rejected", Title: "已否定"},
		{ID: "f_unv", Kind: "host", Status: "unverified", Title: "未核验"},
		{ID: "f_empty", Kind: "host", Title: "旧数据"},
	}
	p := BuildPrompt(db, db.Intents[0], "", "", "", 0, false)
	for _, want := range []string{
		"【未核验线索：note 永不复核",
		"【已否定：不能作为前提】",
		"【未核验·待复核：不是已确认前提",
		"【状态未知：按未核验对待】",
		"### f_note · note",
		"### f_rej · vuln",
	} {
		if !strings.Contains(p, want) {
			t.Fatalf("prompt missing fact status label %q:\n%s", want, p)
		}
	}
	if strings.Contains(p, "【待复核假设：仅供核对") {
		t.Fatalf("prompt still carries the old blanket assumption label:\n%s", p)
	}
}

// TestContextFactsAreReadingChoicesNotPremises pins the context-facts section:
// it is a reading selection, not a premise admission, and its heading names the
// unverified lead role instead of calling every row a pending assumption.
func TestContextFactsAreReadingChoicesNotPremises(t *testing.T) {
	db := testDB()
	db.Intents[0].ContextFactIDs = []string{"f_ctx", "f_note"}
	db.Facts = append(db.Facts,
		model.Fact{ID: "f_ctx", Kind: "host", Status: "unverified", Title: "UNVERIFIED-TITLE", Summary: "UNVERIFIED-SUMMARY"},
		model.Fact{ID: "f_note", Kind: "note", Title: "一条线索"},
	)
	p := BuildPrompt(db, db.Intents[0], "", "", "", 0, false)

	for _, want := range []string{
		"## 参考事实与未核验线索",
		"这些是阅读选择，不是必要前提。",
		"未核验条目只用于验证或反证，不得在叙述中当作已确认结论，也不能代替 from_fact_ids 的准入；note 永不进入复核。",
		"【未核验·待复核：不是已确认前提",
		"【未核验线索：note 永不复核",
	} {
		if !strings.Contains(p, want) {
			t.Fatalf("context-facts section missing %q:\n%s", want, p)
		}
	}
	if strings.Contains(p, "## 参考事实与待复核假设") {
		t.Fatalf("context-facts section still uses the old heading:\n%s", p)
	}
	if !strings.Contains(p, "### f_ctx · host · unverified") || strings.Contains(p, "UNVERIFIED-") {
		t.Fatal("unverified context fact must appear as a placeholder")
	}
	// The premise section (source facts by from_fact_ids) and the context
	// section are different admission channels, in that order.
	premises, context := strings.Index(p, "## 派生自这些事实"), strings.Index(p, "## 参考事实与未核验线索")
	if premises < 0 || context < 0 || premises > context {
		t.Fatalf("premise/context order: premises=%d context=%d\n%s", premises, context, p)
	}
}

// A parked reason often puts the decision at the end ("不再重开"). The brief
// must not cut it at the old 80-rune limit while still bounding the section.
func TestParkedReasonSurvivesTheBriefClip(t *testing.T) {
	db := testDB()
	db.Intents[2].ParkReason = "测定完成：" + strings.Repeat("覆盖矩阵完整，", 6) + "run 末段因网关错误 failed，不再重开。"
	p := BuildPrompt(db, db.Intents[0], "", "", "", 0, false)
	if !strings.Contains(p, "不再重开。") {
		t.Fatalf("brief clipped the park decision:\n%s", p)
	}
}

// With review off there is no verdict to wait for: an unverified fact keeps its
// status and its warning, but its text is shown instead of being replaced by an
// id-only placeholder. Visibility is not promotion.
func TestRenderFactShowsTextWhenReviewIsOff(t *testing.T) {
	f := model.Fact{ID: "f_x", Kind: "host", Status: "unverified", Title: "TITLE-X", Summary: "SUMMARY-X"}
	hidden := renderFact(f, false)
	if strings.Contains(hidden, "TITLE-X") || strings.Contains(hidden, "SUMMARY-X") {
		t.Fatalf("review-on rendering leaked the text: %q", hidden)
	}
	shown := renderFact(f, true)
	for _, want := range []string{"TITLE-X", "SUMMARY-X", "unverified"} {
		if !strings.Contains(shown, want) {
			t.Fatalf("review-off rendering missing %q: %q", want, shown)
		}
	}
	// No review-mode caveat: with the review switched off the row must read
	// like a reviewed fact — the status field is the only difference.
	for _, unwanted := range []string{"复核已关闭", "待复核", "不能代替"} {
		if strings.Contains(shown, unwanted) {
			t.Fatalf("review-off rendering still carries %q: %q", unwanted, shown)
		}
	}
	// A verified fact renders the same either way: the flag only changes what an
	// unverified row shows.
	verified := model.Fact{ID: "f_v", Kind: "host", Status: "verified", Title: "V", Summary: "S"}
	if renderFact(verified, false) != renderFact(verified, true) {
		t.Fatal("verified rendering changed with review visibility")
	}
}
