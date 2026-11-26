// Package ledger turns raw executor events into ledger records.
//
// Since D18 the executor is expected to declare its own facts in
// <workdir>/.saker/facts.jsonl. The event stream is still useful for the run log,
// but it is a poor fact source: guessing facts out of tool arguments is how a
// board fills up with `bash -c ...`. What remains here are the derived records
// the ledger can prove by looking at the filesystem.
package ledger

import (
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kksty/saker/internal/evidence"
	"github.com/kksty/saker/internal/executor"
	"github.com/kksty/saker/internal/model"
	"github.com/kksty/saker/internal/store"
)

type Ingestor struct {
	Store       *store.Store
	MaxEvidence int
}

var errStopWalk = errors.New("stop")
var errEvidenceStorage = errors.New("证据账本写入失败")

// derivedProductsNote is the one derived note a completed run leaves behind.
// The title is the identity of that row: a later run on the same direction
// refreshes it instead of adding a duplicate.
const derivedProductsNote = "本次产出文件"

// OnFinish settles one run: declared facts, then evidence, then the derived
// loot row. It returns how many lines the executor declared, which is the only
// observable measure of "did it honour the handoff contract". A playbook line
// counts: it is a valid declaration, just not a fact.
//
// Prose is deliberately not promoted to a fact. The run log already has it, and
// a note called "执行者自述" made every failed or canceled attempt look like a
// finding on the board (and a second, identical-looking node in the graph).
func (g *Ingestor) OnFinish(runID, intentID, caseRoot, workDir string, since time.Time) int {
	// A batch can be replayed after a partial commit. Do not consume any of it
	// unless every durable declaration write succeeded; admissions are idempotent.
	consumedBefore := Consumed(workDir)
	committed := true
	// failures names every item that kept the batch from being committed. The
	// run must not say only "not fully persisted": the operator needs the
	// offending declaration/playbook/path to fix it (task-17).
	var failures []string
	fail := func(format string, args ...any) {
		failures = append(failures, fmt.Sprintf(format, args...))
	}
	decl, err := ReadDeclaredSince(caseRoot, workDir)
	if err != nil {
		committed = false
		fail("读交接文件失败：%v", err)
		log.Printf("run %s：读交接文件失败（已落库的部分保留，下一轮会重读）：%v", runID, err)
	}
	if decl.Truncated {
		// The cap is a per-run budget, not a silent delete: the unread tail stays
		// in the handoff (Consumed stops before it) and the operator can see why
		// declared_facts stopped at the cap.
		log.Printf("run %s：交接超过每次 %d 条，后续行留在 facts.jsonl 未消费", runID, MaxDeclaredFacts)
	}
	if decl.Bad > 0 {
		// The skipped lines already advanced the read offset (declare.go:328-329),
		// so the next run never sees them again: an executor that believed it had
		// declared something has to learn that it did not, in the run's own
		// warnings rather than a log line nobody reads. The per-line cause rides
		// decl.Warnings (lines that cannot be decoded at all say which one and
		// why); this sentence is the shared outcome and the way to recover.
		g.recordWarnings(runID, []string{fmt.Sprintf(
			"交接有 %d 行无法解析或越界已跳过：这些行已计入读取位置，下一轮不会重读；要入账请在新行重新声明（证据路径以本轮 case 目录为基准，不能是绝对路径或 ../../x）",
			decl.Bad)})
	}

	// A playbook line is knowledge, not a finding: it is routed to the playbook
	// table and never becomes a fact, so it cannot inflate the board, enter a
	// report or be sent for review (D70).
	var facts, books []DeclaredFact
	for _, df := range decl.Facts {
		if df.Kind == "playbook" {
			books = append(books, df)
			continue
		}
		facts = append(facts, df)
	}

	// Declared evidence is registered first: a fact's own proof must never lose
	// the cap to a work dir full of scratch files, which is exactly what a
	// reverse-engineering session produces.
	byRel := make(map[string]string, 16)
	var declaredNames []string
	// Handoff problems are collected rather than swallowed: a path that does not
	// resolve (out of bounds, gone) is exactly what the next round must correct,
	// and the run row is where the operator looks (task-17).
	warnings := append([]string(nil), decl.Warnings...)
	infos := append([]string(nil), decl.Infos...)
	for _, df := range facts {
		for _, rel := range df.Evidence {
			if _, ok := byRel[rel]; ok {
				continue
			}
			id, err := g.addEvidenceFor(runID, intentID, caseRoot, workDir, rel, df.From[rel])
			if err != nil {
				if errors.Is(err, errEvidenceStorage) {
					committed = false
					fail("证据「%s」无法写入账本：%v", clipRunes(rel, 120), err)
				}
				warnings = append(warnings, fmt.Sprintf(
					"事实「%s」的证据「%s」无法登记：%v",
					clipRunes(df.Title, 80), clipRunes(rel, 120), err))
				continue
			}
			byRel[rel] = id
			declaredNames = append(declaredNames,
				caseURI(caseRoot, filepath.Join(workDir, filepath.FromSlash(rel))))
		}
	}

	harvested, harvestWarnings, harvestFailure := g.harvest(runID, intentID, caseRoot, workDir, since, byRel)
	if harvestFailure {
		committed = false
		fail("自动证据登记发生存储写入失败")
	}
	warnings = append(warnings, harvestWarnings...)
	g.recordWarnings(runID, warnings)
	g.recordInfos(runID, infos)

	names := append(declaredNames, harvested...)

	// Every write below is best-effort for the run's outcome, but a swallowed
	// error means the run row says "declared N" while the ledger kept nothing.
	// Log it: the count is the contract metric, so it must not lie silently.
	warn := func(what string, err error) {
		if err != nil {
			committed = false
			fail("%s：%v", what, err)
			log.Printf("run %s：%s 失败：%v", runID, what, err)
		}
	}

	// Summary and doc land as declared. The ledger is the full view: the
	// console's fact panel and GET /api/facts/{id} show this text, and a budget
	// belongs to the projection that needs a short row (dispatch prompt 400
	// runes, graph.json 240, commander board 240), not to storage. A 600-rune
	// clip here was a second, silent copy of that budget: a real finding ended
	// mid-word ("…Conte…") before any reader could see the rest (D114).
	//
	// The upsert is shared with the live watcher (ApplyDeclaration), so the
	// finish settlement and the mid-run view agree on identity and fields. This
	// path passes the preserved evidence ids it just admitted: it is the
	// authoritative evidence set for the declaration.
	for _, df := range facts {
		ids := make([]string, 0, len(df.Evidence))
		for _, rel := range df.Evidence {
			if id, ok := byRel[rel]; ok {
				ids = append(ids, id)
			}
		}
		if _, err := ApplyDeclaration(g.Store, intentID, runID, df, ids); err != nil {
			warn("落事实 "+df.Title, err)
		}
	}

	if len(names) > 0 {
		// The ledger can prove these files exist, not that they are loot: from
		// the filesystem a scratch file and a stolen dump look the same. Loot
		// and artifact are the executor's call to declare; this is a derived
		// note listing what the run left behind.
		//
		// One note per direction, refreshed: a retried or steered intent runs
		// several times, and appending a second "本次产出文件" each time filled
		// the board with near-identical rows (16 of them in one real project).
		// The previous run's files are already evidence rows, so replacing the
		// text loses nothing.
		summary, doc := strings.Join(names, ", "), strings.Join(names, "\n")
		if existing, ok := g.Store.FindDerivedFact(intentID, "note", derivedProductsNote); ok {
			_, err := g.Store.UpdateFact(existing.ID, func(f *model.Fact) {
				f.SourceRunID = runID
				f.Summary = summary
				f.Doc = doc
				f.DeclaredConfidence = "observed"
			})
			warn("刷新产出文件 note", err)
		} else {
			_, err := g.Store.AddFact(model.Fact{
				IntentID:    intentID,
				SourceRunID: runID,
				Kind:        "note",
				Title:       derivedProductsNote,
				Summary:     summary,
				Doc:         doc,
				Confidence:  "observed",
				Origin:      model.OriginDerived,
			})
			warn("落产出文件 note", err)
		}
	}
	// The run that used the tactic is the best writer: it has the commands, the
	// output and the failure mode. Its declaration becomes a confirmed entry;
	// the commander can only nominate candidates (D70).
	//
	// "Confirmed" is the writer's declaration, not a verdict: the sources admitted
	// earlier in this settlement are unverified until the review runs later, and
	// the review does not touch playbooks. Checking them here would freeze every
	// legitimate tactic as a candidate forever, so the write stays as declared and
	// the read side derives the label (model.Playbook.Confirmed). The warning is
	// what tells the run why this tactic will be recalled as a candidate.
	knownFacts := model.FactIndex(g.Store.Snapshot().Facts)
	for _, df := range books {
		valid := true
		resolved := make([]string, 0, len(df.FromFactIDs))
		for _, id := range df.FromFactIDs {
			if factID, ok := ResolveFactReference(g.Store, intentID, id); ok {
				resolved = append(resolved, factID)
			} else {
				g.recordWarnings(runID, []string{fmt.Sprintf("跳过打法「%s」：来源 %s 不是账本事实 ID，且 declaration_id/source_ref 未能唯一解析；用 GET /api/facts?intent=%s 查询 f_ ID；本意图候选：%s",
					clipRunes(df.Title, 80), clipRunes(id, 120), intentID, factReferenceCandidates(g.Store, intentID))})
				valid = false
			}
		}
		if !valid {
			continue
		}
		pb, _, err := g.Store.UpsertPlaybook(model.Playbook{
			SourceRunID:  runID,
			FromFactIDs:  resolved,
			Pattern:      df.Pattern,
			RequiredTags: df.RequiredTags,
			Title:        df.Title,
			When:         clipRunes(df.Summary, 300),
			Doc:          clipRunes(df.Doc, 4000),
			Tags:         df.Tags,
			Outcome:      df.Outcome,
			Origin:       model.PlaybookFromExecutor,
			Status:       model.PlaybookConfirmed,
			IntentID:     intentID,
		})
		warn("落打法 "+df.Title, err)
		if err == nil && !pb.Confirmed(knownFacts) {
			detail := pb.SourceProblem(knownFacts)
			if detail != "" {
				detail = "（" + detail + "）"
			}
			g.recordWarnings(runID, []string{fmt.Sprintf(
				"打法「%s」已落库，但来源未核验或无证据%s，召回/派发按候选显示；核实来源后自动恢复确认",
				clipRunes(pb.Title, 80), detail)})
		}
	}
	// Only now: the run is settled, so the next turn starts after these lines.
	// The offset is the last line actually read; a truncated handoff leaves its
	// tail for the next run instead of skipping it forever.
	if committed {
		warn("记录交接游标", CommitDeclared(workDir, decl.Consumed))
	}
	if !committed {
		detail := clipRunes(strings.Join(failures, "；"), 1800)
		if detail == "" {
			detail = "未知写入失败"
		}
		g.recordWarnings(runID, []string{fmt.Sprintf(
			"交接未全部持久化：%s；保留消费游标 %d（修复后幂等重读本批声明）", detail, consumedBefore)})
	}
	// Playbooks count as handoff lines: a run that only wrote one still honoured
	// the contract, and declared_facts is the only measure of that.
	return len(facts) + len(books)
}

// harvest records the files a run left in its work dir as evidence and returns
// their case-relative names. byRel already holds the declared evidence, which
// is never re-registered and never counted twice.
//
// A file whose bytes cannot be preserved is skipped and reported as a warning:
// a derived "these files exist" note must not point at evidence the ledger
// could not take responsibility for.
func (g *Ingestor) harvest(runID, intentID, caseRoot, workDir string, since time.Time, byRel map[string]string) ([]string, []string, bool) {
	max := g.MaxEvidence
	if max <= 0 {
		max = 200
	}
	budget := max - len(byRel)
	if budget <= 0 {
		return nil, nil, false
	}
	var names, warnings []string
	storageFailed := false

	err := filepath.WalkDir(workDir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			switch d.Name() {
			// .evidence is our own store: harvesting it would only re-admit
			// preserved objects (it sits above the work dir, but a caller could
			// pass the same path for both).
			case ".cyber", ".git", "node_modules", HandoffDir, ".verify", ".evidence", executor.SessionDir:
				return filepath.SkipDir
			}
			return nil
		}
		// The run's own trajectory is evidence *about* the run, not a file the
		// run produced; counting it would spend a slot of the evidence budget on
		// every dispatch (D36/D42).
		if d.Name() == executor.SessionFile || strings.HasSuffix(d.Name(), ".aop.jsonl") {
			return nil
		}
		relWork, err := filepath.Rel(workDir, p)
		if err != nil {
			return nil
		}
		rel := filepath.ToSlash(relWork)
		if _, done := byRel[rel]; done {
			return nil
		}
		if len(names) >= budget {
			return errStopWalk
		}
		info, err := d.Info()
		if err != nil || info.ModTime().Before(since) {
			return nil
		}
		id, err := g.addEvidenceFor(runID, intentID, caseRoot, workDir, rel, "")
		if err != nil {
			storageFailed = storageFailed || errors.Is(err, errEvidenceStorage)
			warnings = append(warnings, fmt.Sprintf(
				"本轮产出文件「%s」无法登记：%v", clipRunes(rel, 120), err))
			return nil
		}
		byRel[rel] = id
		names = append(names, caseURI(caseRoot, p))
		return nil
	})
	if err != nil && !errors.Is(err, errStopWalk) {
		return names, warnings, storageFailed
	}
	return names, warnings, storageFailed
}

// addEvidenceFor preserves one file's exact bytes and records the evidence row.
//
// Preservation is the admission contract, not a best-effort side effect: if the
// bytes cannot be copied into the immutable store we refuse the evidence and
// return why. Falling back to the mutable working path would recreate the F2
// failure — a later retry overwrites that path, the fact stays verified, and its
// "original" silently becomes different bytes.
func (g *Ingestor) addEvidenceFor(runID, intentID, caseRoot, workDir, rel, from string) (string, error) {
	abs := filepath.Join(workDir, filepath.FromSlash(rel))
	st, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("文件在结算时已不可读：%w", err)
	}
	if st.IsDir() {
		return "", fmt.Errorf("是目录，不是文件")
	}
	preserved, err := evidence.Preserve(caseRoot, abs)
	if err != nil {
		var sourceError *os.PathError
		if os.IsNotExist(err) || errors.As(err, &sourceError) && sourceError.Path == abs {
			return "", fmt.Errorf("原件不可读：%w", err)
		}
		return "", fmt.Errorf("%w：无法保全原件：%w", errEvidenceStorage, err)
	}
	e, err := g.Store.AddEvidence(model.Evidence{
		RunID:     runID,
		IntentID:  intentID,
		Name:      filepath.Base(abs),
		Mime:      mimeByExt(abs),
		Size:      preserved.Size,
		URI:       preserved.URI,
		Digest:    preserved.Digest,
		SourceURI: evidence.OriginPath(caseRoot, abs),
		From:      from,
	})
	if err != nil {
		return "", fmt.Errorf("%w: %w", errEvidenceStorage, err)
	}
	return e.ID, nil
}

// ResolveFactReference accepts an exact ledger identity first. Aliases are
// scoped to the declaring direction and must resolve uniquely; no fuzzy title
// or cross-project matching can turn an unrelated observation into a premise.
func ResolveFactReference(st *store.Store, intentID, ref string) (string, bool) {
	ref = strings.TrimSpace(ref)
	if f, ok := st.FactByID(ref); ok {
		return f.ID, true
	}
	return resolveFactAlias(st.ListFacts(intentID, 0), intentID, ref)
}

func resolveFactAlias(facts []model.Fact, intentID, ref string) (string, bool) {
	normalize := func(s string) string {
		for _, prefix := range []string{"decl_", "decl-", "d_"} {
			if strings.HasPrefix(s, prefix) {
				return strings.TrimPrefix(s, prefix)
			}
		}
		return s
	}
	var found string
	for _, f := range facts {
		if f.IntentID != intentID {
			continue
		}
		match := f.SourceRef != "" && f.SourceRef == ref || f.DeclarationID != "" && normalize(f.DeclarationID) == normalize(ref)
		if match {
			if found != "" && found != f.ID {
				return "", false
			}
			found = f.ID
		}
	}
	return found, found != ""
}

func factReferenceCandidates(st *store.Store, intentID string) string {
	var out []string
	for _, f := range st.ListFacts(intentID, 0) {
		if f.DeclarationID != "" {
			out = append(out, f.DeclarationID+" → "+f.ID)
		}
		if len(out) == 8 {
			break
		}
	}
	return strings.Join(out, ", ")
}

func caseURI(caseRoot, p string) string {
	rel, err := filepath.Rel(caseRoot, p)
	if err != nil {
		rel = p
	}
	return filepath.ToSlash(rel)
}

// recordWarnings writes the handoff problems onto the run row (deduped, bounded)
// and keeps them in the log. Warnings are diagnostics and the next round's hint;
// they never change the run's outcome and never replace the error path.
func (g *Ingestor) recordWarnings(runID string, warnings []string) {
	if g.Store == nil || len(warnings) == 0 {
		return
	}
	for _, w := range warnings {
		log.Printf("run %s：交接告警：%s", runID, w)
	}
	if _, err := g.Store.UpdateRun(runID, func(r *model.Run) {
		r.Warnings = mergeWarnings(r.Warnings, warnings)
	}); err != nil {
		log.Printf("run %s：写交接告警失败：%v", runID, err)
	}
}

// recordInfos writes non-problem handoff notes (today: expected in-batch
// corrections) to the run row. They are kept separate from Warnings so the
// status page's warning count and the next round's "previous handoff problems"
// section stay about things that need fixing.
func (g *Ingestor) recordInfos(runID string, infos []string) {
	if g.Store == nil || len(infos) == 0 {
		return
	}
	for _, info := range infos {
		log.Printf("run %s：交接提示：%s", runID, info)
	}
	if _, err := g.Store.UpdateRun(runID, func(r *model.Run) {
		r.Infos = mergeWarnings(r.Infos, infos)
	}); err != nil {
		log.Printf("run %s：写交接提示失败：%v", runID, err)
	}
}

// mergeWarnings appends incoming to existing, dropping blanks and duplicates and
// stopping at model.MaxRunWarnings. A retried or re-settled run must not grow its
// warning list twice, and the bound is the same one the API's appendRunWarning
// enforces: one number for the whole binary, not one per package.
func mergeWarnings(existing, incoming []string) []string {
	out := make([]string, 0, len(existing)+len(incoming))
	seen := make(map[string]bool, len(existing)+len(incoming))
	for _, w := range append(append([]string(nil), existing...), incoming...) {
		w = strings.TrimSpace(w)
		if w == "" || seen[w] {
			continue
		}
		seen[w] = true
		out = append(out, w)
		if len(out) >= model.MaxRunWarnings {
			break
		}
	}
	return out
}

func clipRunes(s string, n int) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func mimeByExt(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".html", ".htm":
		return "text/html"
	case ".json":
		return "application/json"
	case ".md":
		return "text/markdown"
	case ".txt", ".log":
		return "text/plain"
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".pdf":
		return "application/pdf"
	default:
		return "application/octet-stream"
	}
}
