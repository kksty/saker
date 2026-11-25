package report

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/store"
)

// Validate checks a finished report against the invariants that make it
// deliverable (docs/08, D26). Rules are structural, never stylistic: they must
// be things the model cannot argue its way around.
func Validate(dir string, db store.DB, staleAfter time.Duration) []Check {
	var checks []Check
	add := func(rule string, ok bool, detail string) {
		checks = append(checks, Check{Rule: rule, OK: ok, Detail: detail})
	}
	for _, goal := range db.Goals {
		if !goal.Closed && goal.ReceiptStale {
			add("goal_receipt:"+goal.ID, false, "目标完成依据已变化，需要重新验收；历史收据保留")
		}
	}

	// 1. The prose exists. A report without an index is not a report.
	if st, err := os.Stat(filepath.Join(dir, IndexFile)); err != nil || st.Size() == 0 {
		add("index_md", false, "缺少 index.md 或内容为空")
	} else {
		add("index_md", true, "")
	}

	// 2. findings.json parses into the agreed shape. A parse failure does not
	// stop the run: the remaining rules still report what else is missing.
	findings, err := readFindings(dir)
	if err != nil {
		add("findings_json", false, err.Error())
		findings = nil
	} else {
		add("findings_json", true, "")
	}

	// 3. Every conclusion that would be reported has a finding row.
	reported := reportable(db.Facts)
	byID := make(map[string]model.Fact, len(db.Facts))
	for _, f := range db.Facts {
		byID[f.ID] = f
	}
	have := make(map[string]Finding, len(findings))
	for _, f := range findings {
		have[f.Fact] = f
	}
	var absent []string
	for _, f := range reported {
		if _, ok := have[f.ID]; !ok {
			absent = append(absent, f.ID+" "+f.Title)
		}
	}
	add("findings_cover_reportable", len(absent) == 0, "缺少结论："+strings.Join(absent, "；"))

	// 4. Every row names a fact that exists and may be reported. Without this a
	// row can point at an id nobody ever recorded — a claim attached to nothing —
	// and still pass every other rule, because ownership is only checked for
	// facts that resolve. A rejected fact has no row either: its negative result
	// belongs in the fact's doc and the report's limitations, not here (D30).
	var stray []string
	usable := make(map[string]bool, len(reported))
	for _, f := range reported {
		usable[f.ID] = true
	}
	for _, f := range findings {
		if !usable[f.Fact] {
			stray = append(stray, f.Fact)
		}
	}
	add("fact_exists", len(stray) == 0, "图谱里没有或不可报告："+strings.Join(stray, "；"))

	// 5. Verdicts come from the two-state enum (D41).
	var badVerdict []string
	for _, f := range findings {
		switch f.Verdict {
		case VerdictCandidate, VerdictConfirmed:
		default:
			badVerdict = append(badVerdict, f.Fact+"="+f.Verdict)
		}
	}
	add("verdict_enum", len(badVerdict) == 0, "不合法："+strings.Join(badVerdict, "；"))

	// 5b. A report may not grade a conclusion higher than the ledger holds: the
	// writer copies the snapshot's label, and over-reporting sends the operator
	// to fix the wrong thing first. Under-reporting and an unrecognised word are
	// writing errors of the same weight as "copied too low": they are recorded in
	// the detail with the raw value, but do not block delivery. An unrecognised
	// word is identified BEFORE the rank comparison; otherwise rank 0 would make
	// it look like a conservative under-report and hide the bad value.
	var over, under, invalid []string
	for _, f := range findings {
		if !usable[f.Fact] {
			continue // fact_exists already reports a row that resolves to nothing
		}
		fact := byID[f.Fact]
		raw := strings.TrimSpace(f.Severity)
		normalized := model.NormalizeSeverity(raw)
		if raw != "" && normalized == "" {
			invalid = append(invalid, f.Fact+" 账本 "+severityLabel(fact.Severity)+" → 报告 \""+raw+"\"（无法识别）")
			continue
		}
		want, got := model.SeverityRank(fact.Severity), model.SeverityRank(normalized)
		switch {
		case got > want:
			over = append(over, f.Fact+" 账本 "+severityLabel(fact.Severity)+" → 报告 "+severityLabel(normalized))
		case got < want:
			under = append(under, f.Fact+" 账本 "+severityLabel(fact.Severity)+" → 报告 "+severityLabel(normalized))
		}
	}
	var detail []string
	if len(over) > 0 {
		detail = append(detail, "报告等级高于账本："+strings.Join(over, "；"))
	}
	if len(under) > 0 {
		detail = append(detail, "低报（不拦截交付）："+strings.Join(under, "；"))
	}
	if len(invalid) > 0 {
		detail = append(detail, "无法识别的等级（不拦截交付，按抄写错误记录）："+strings.Join(invalid, "；"))
	}
	add("severity_not_above_ledger", len(over) == 0, strings.Join(detail, "；"))

	// 6. confirmed needs all five fields: a half-described finding is a rumour.
	var thin []string
	for _, f := range findings {
		if f.Verdict != VerdictConfirmed {
			continue
		}
		var lost []string
		if strings.TrimSpace(f.Location) == "" {
			lost = append(lost, "location")
		}
		if strings.TrimSpace(f.Preconditions) == "" {
			lost = append(lost, "preconditions")
		}
		if strings.TrimSpace(f.Impact) == "" {
			lost = append(lost, "impact")
		}
		if strings.TrimSpace(f.Trace) == "" {
			lost = append(lost, "trace")
		}
		if len(f.Evidence) == 0 {
			lost = append(lost, "evidence")
		}
		if len(lost) > 0 {
			thin = append(thin, f.Fact+" 缺 "+strings.Join(lost, "/"))
		}
	}
	add("confirmed_fields", len(thin) == 0, strings.Join(thin, "；"))

	// 6b. "confirmed" is a verdict about the ledger, not a writing style: the
	// fact must actually be verified. Without this, a model could dress an
	// unverified candidate in the five fields and pass it off as proven (D55).
	var unverified []string
	for _, f := range findings {
		if f.Verdict != VerdictConfirmed {
			continue
		}
		fact, ok := byID[f.Fact]
		if !ok || fact.Status != "verified" {
			unverified = append(unverified, f.Fact)
		}
	}
	add("confirmed_verified", len(unverified) == 0, "图谱未复核却写成 confirmed："+strings.Join(unverified, "；"))

	// 7. Every evidence path resolves to a real file inside the report.
	var dangling []string
	for _, f := range findings {
		for _, rel := range f.Evidence {
			abs, ok := safeJoin(dir, rel)
			if !ok {
				dangling = append(dangling, f.Fact+" 路径越界 "+rel)
				continue
			}
			if st, err := os.Stat(abs); err != nil || st.IsDir() {
				dangling = append(dangling, f.Fact+" 证据不存在 "+rel)
				continue
			}
			// The evidence must belong to the fact it is attached to: a report
			// that swaps two proofs is worse than one with no proof at all.
			if fact, ok := byID[f.Fact]; ok && !ownedBy(db, fact, rel) {
				dangling = append(dangling, f.Fact+" 证据不属于该事实 "+rel)
			}
		}
	}
	add("evidence_inside", len(dangling) == 0, strings.Join(dangling, "；"))

	// 8. Each confirmed finding has its own page, and the page links its proof.
	// The path is model-written (findings.json), so it goes through safeJoin:
	// `../..` in f.Fact used to make this rule read a file outside the report
	// directory and turn the check into a filesystem probe.
	var noPage []string
	for _, f := range findings {
		if f.Verdict != VerdictConfirmed {
			continue
		}
		page, ok := safeJoin(dir, FindingsDir+"/"+f.Fact+".md")
		if !ok {
			noPage = append(noPage, f.Fact+" 路径越界")
			continue
		}
		body, err := os.ReadFile(page)
		if err != nil {
			noPage = append(noPage, f.Fact)
			continue
		}
		for _, rel := range f.Evidence {
			if !strings.Contains(string(body), rel) {
				noPage = append(noPage, f.Fact+" 的正文没有链接 "+rel)
			}
		}
	}
	add("finding_pages", len(noPage) == 0, strings.Join(noPage, "；"))

	// 9. Freshness, actually evaluated against now (D27). A stale report is not
	// a warning: it must not read as deliverable.
	if d := staleness(db, staleAfter); d == "" {
		add("freshness", true, "")
	} else {
		add("freshness", false, d)
	}
	return checks
}

// ownedBy reports whether rel (a report-relative evidence path) is one of the
// fact's evidence files. Ownership is checked by the evidence id prefix that
// copyEvidence writes.
func ownedBy(db store.DB, fact model.Fact, rel string) bool {
	base := filepath.Base(rel)
	for _, eid := range fact.EvidenceIDs {
		for _, e := range db.Evidence {
			if e.ID == eid && strings.HasPrefix(base, eid+"-") {
				return true
			}
		}
	}
	return false
}

// severityLabel renders a grade for a check detail; empty means ungraded and
// must not be shown as "low".
func severityLabel(sev string) string {
	if strings.TrimSpace(sev) == "" {
		return "未标注"
	}
	return strings.TrimSpace(sev)
}

func safeJoin(dir, rel string) (string, bool) {
	rel = strings.TrimSpace(strings.TrimPrefix(rel, "./"))
	if rel == "" || filepath.IsAbs(rel) || strings.HasPrefix(rel, "/") || strings.HasPrefix(rel, `\`) {
		return "", false
	}
	// A Windows drive-relative path ("C:x") is not caught by IsAbs.
	if vol := filepath.VolumeName(rel); vol != "" {
		return "", false
	}
	abs := filepath.Join(dir, filepath.FromSlash(rel))
	out, err := filepath.Rel(dir, abs)
	if err != nil || out == ".." || strings.HasPrefix(out, ".."+string(filepath.Separator)) {
		return "", false
	}
	return abs, true
}

// staleness returns a non-empty detail when something in the report is too old
// to stand behind. It is a check, not a warning: the status becomes incomplete.
//
// The clock starts at the fact's creation OR its most recent passing review.
// UpdateFact deliberately keeps CreatedAt, so before this a 31-day-old fact
// stayed stale forever even after a verifier reproduced it today: there was no
// data path out of "over stale_after". A rejected fact never reaches a report,
// and needs_evidence is not a re-test (the fact stays unverified), so only a
// pass moves the clock.
func staleness(db store.DB, staleAfter time.Duration) string {
	if staleAfter <= 0 {
		return ""
	}
	now := time.Now()
	reviews := reviewsByFact(db.Reviews)
	var stale []string
	for _, f := range reportable(db.Facts) {
		anchoredAt := f.CreatedAt
		if f.ObservedAt != nil {
			anchoredAt = *f.ObservedAt
		}
		if r, ok := model.LatestReviewForClaim(f, reviews[f.ID]); ok && r.Verdict == "pass" && r.CreatedAt.After(anchoredAt) {
			anchoredAt = r.CreatedAt
		}
		if now.Sub(anchoredAt) > staleAfter {
			stale = append(stale, f.ID+" 最近观测或复核于 "+anchoredAt.Format("2006-01-02"))
		}
	}
	if len(stale) > 0 {
		return fmt.Sprintf("超过 %d 天的证据需要重测：%s", int(staleAfter.Hours()/24), strings.Join(stale, "；"))
	}
	return ""
}

// ReadFindings is the read-only accessor for one report's finding rows. The
// control plane compares the fact ids a report cites against the ledger, and it
// must read the same file the validator reads: a second parser would let the
// console's answer and the report's own checks disagree about one format.
func ReadFindings(dir string) ([]Finding, error) { return readFindings(dir) }

func readFindings(dir string) ([]Finding, error) {
	b, err := os.ReadFile(filepath.Join(dir, Findings))
	if err != nil {
		return nil, fmt.Errorf("缺少 %s", Findings)
	}
	var out []Finding
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, fmt.Errorf("%s 不是合法的 JSON 数组", Findings)
	}
	return out, nil
}

// BuildPrompt is the whole brief of a report run. It carries the material, the
// three artifacts and the hard invariants — the ones the code also checks.
// The outline (sections, order, wording, length) lives in the tianwu-report
// skill, which travels into the same turn prompt; restating it here is how the
// two drift apart, and the old "覆盖表" line was a leftover of a design D30
// already removed (D92).
func BuildPrompt(findings int, scope string) string {
	var b strings.Builder
	b.WriteString("你是报告撰写者。一次 run 只做一件事：把当前目录的事实写成一份能直接交给客户的报告。\n")
	b.WriteString("不做测试、不联网、不改 " + EvidenceDir + "/，也不写 " + Snapshot + " 之外的事实。\n\n")

	b.WriteString("## 材料（都在当前目录）\n")
	b.WriteString("- " + Snapshot + "：本次要写的事实、目标、范围（唯一的结论来源）；每条事实带它最新的一条 `review`（verdict/notes），那是复核者的判断\n")
	b.WriteString("- " + Snapshot + " 的 `rejected`：复核驳回的结论（vuln/cred 且 status=rejected），不写 findings 行，只在「5.2 止于何处」写清否定的原因\n")
	b.WriteString("- report.json：本报告的编号与生成时间，写进 " + IndexFile + " 的元信息行\n")
	b.WriteString("- " + EvidenceDir + "/：证据原文件，文件名形如 <证据id>-<原名>\n")
	fmt.Fprintf(&b, "- 可写结论 %d 条\n\n", findings)
	if scope != "" {
		b.WriteString("授权范围：" + scope + "\n\n")
	}

	b.WriteString("## 产物\n")
	b.WriteString("按技能 tianwu-report 的体例写这三个文件：" + Findings + "、" + IndexFile + "、" +
		FindingsDir + "/<fact>.md；章节、顺序、写法、篇幅都按该技能执行，不增、不减、不换序。\n\n")

	b.WriteString("## 硬约束\n")
	b.WriteString("- 只写 " + Snapshot + " 里的事实；`candidate` 在「4. 待复核结论」要写明它最新 review 的 verdict 与没通过的原因（取 review.notes 的判断，不抄证据细节）。\n")
	b.WriteString("- 被 rejected 的结论不写 findings 行，否定原因写进「5.2 止于何处」。\n")
	b.WriteString("- `confirmed` 只能给图谱里 status=verified 的事实，其余写 `candidate`。\n")
	b.WriteString("- `confirmed` 的 location / preconditions / impact / trace / evidence 一处不能空。\n")
	b.WriteString("- `severity` 只从 snapshot 抄，不得自评或改写；`findings.json` 与正文按 `critical > high > medium > low > 未标注` 排序。\n")
	b.WriteString("- `evidence` 只能引用 " + EvidenceDir + "/ 下真实存在、且属于这条事实的文件。\n")
	b.WriteString("- `refs` 不是证据：只能写进「范围与限制」，不能冒充证据。\n")
	return b.String()
}
