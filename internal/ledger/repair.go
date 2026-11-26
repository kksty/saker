package ledger

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"github.com/kksty/saker/internal/evidence"
	"github.com/kksty/saker/internal/store"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
)

// CursorRepair only advances past declarations already preserved verbatim in
// the ledger. It never re-applies historical claims or invalidates reviews.
type CursorRepair struct {
	Before    int64    `json:"before"`
	Through   int64    `json:"through"`
	SHA256    string   `json:"sha256"`
	Matched   int      `json:"matched"`
	Playbooks int      `json:"playbooks"`
	Problems  []string `json:"problems,omitempty"`
	Warnings  []string `json:"warnings,omitempty"`
}

func PlanCursorRepair(db store.DB, caseRoot, workDir, intentID string) (CursorRepair, error) {
	p := CursorRepair{Before: Consumed(workDir)}
	raw, err := os.ReadFile(filepath.Join(workDir, HandoffDir, HandoffFile))
	if err != nil {
		return p, err
	}
	hash := sha256.Sum256(raw)
	p.SHA256 = hex.EncodeToString(hash[:])
	decl, err := ReadDeclaredSince(caseRoot, workDir)
	if err != nil {
		return p, err
	}
	p.Through = decl.Consumed
	p.Warnings = decl.Warnings
	for _, df := range decl.Facts {
		if df.Kind == "playbook" {
			p.Playbooks++
			if problem := checkRepairPlaybook(db, intentID, df); problem != "" {
				p.Problems = append(p.Problems, problem)
			}
			continue
		}
		matches := 0
		for _, f := range db.Facts {
			if f.IntentID != intentID {
				continue
			}
			identity := df.SourceRef != "" && f.SourceRef == df.SourceRef || df.DeclarationID != "" && f.DeclarationID == df.DeclarationID
			if !identity {
				continue
			}
			matches++
			next := declarationUpdate(f, df, f.SourceRunID, strings.TrimSpace(df.Summary), nil)
			if !sameDeclarationFields(f, next) {
				p.Problems = append(p.Problems, fmt.Sprintf("声明 %s 与账本 %s 的当前断言不同，拒绝跳过", df.DeclarationID, f.ID))
			}
			for _, rel := range df.Evidence {
				content, readErr := os.ReadFile(filepath.Join(workDir, filepath.FromSlash(rel)))
				digest := sha256.Sum256(content)
				source := evidence.OriginPath(caseRoot, filepath.Join(workDir, filepath.FromSlash(rel)))
				kept := false
				for _, e := range db.Evidence {
					if slices.Contains(f.EvidenceIDs, e.ID) && e.SourceURI == source && e.Digest == hex.EncodeToString(digest[:]) {
						kept = true
						break
					}
				}
				if readErr != nil || !kept {
					p.Problems = append(p.Problems, fmt.Sprintf("声明 %s 的证据 %s 尚未按原件摘要保全，拒绝跳过", df.DeclarationID, rel))
				}
			}
		}
		if matches != 1 {
			p.Problems = append(p.Problems, fmt.Sprintf("声明 %s 尚未唯一落库（匹配 %d），拒绝跳过", df.DeclarationID, matches))
		} else {
			p.Matched++
		}
	}
	return p, nil
}

func CommitCursorRepair(workDir string, p CursorRepair) error {
	if len(p.Problems) > 0 {
		return fmt.Errorf("仍有 %d 条声明未经核对", len(p.Problems))
	}
	if Consumed(workDir) != p.Before {
		return fmt.Errorf("消费游标已变化，请重新检查")
	}
	raw, err := os.ReadFile(filepath.Join(workDir, HandoffDir, HandoffFile))
	if err != nil {
		return err
	}
	hash := sha256.Sum256(raw)
	if hex.EncodeToString(hash[:]) != p.SHA256 {
		return fmt.Errorf("交接文件已变化，请重新检查")
	}
	return CommitDeclared(workDir, p.Through)
}

// A repair never discards a valid, uncommitted playbook. Invalid references are
// not automatically reinterpreted; normal settlement can skip them with its
// diagnostic, while the offline repair remains conservative.
func checkRepairPlaybook(db store.DB, intentID string, df DeclaredFact) string {
	var refs []string
	for _, ref := range df.FromFactIDs {
		id := ""
		for _, f := range db.Facts {
			if f.ID == strings.TrimSpace(ref) {
				id = f.ID
				break
			}
		}
		if id == "" {
			id, _ = resolveFactAlias(db.Facts, intentID, strings.TrimSpace(ref))
		}
		if id == "" {
			return fmt.Sprintf("打法 %s 的来源 %s 无法核对，请先由正常结算记录跳过原因", df.Title, ref)
		}
		refs = append(refs, id)
	}
	for _, book := range db.Playbooks {
		if book.Title != strings.TrimSpace(df.Title) || book.IntentID != intentID {
			continue
		}
		if book.When != clipRunes(df.Summary, 300) || book.Doc != clipRunes(df.Doc, 4000) || book.Outcome != df.Outcome || book.Origin != "executor" || book.Status != "confirmed" {
			continue
		}
		if !reflect.DeepEqual(book.Pattern, df.Pattern) {
			continue
		}
		kept := true
		for _, id := range refs {
			if !slices.Contains(book.FromFactIDs, id) {
				kept = false
			}
		}
		for _, tag := range df.Tags {
			if !slices.Contains(book.Tags, strings.TrimSpace(tag)) {
				kept = false
			}
		}
		for _, tag := range df.RequiredTags {
			if !slices.Contains(book.RequiredTags, strings.TrimSpace(tag)) {
				kept = false
			}
		}
		if kept {
			return ""
		}
	}
	return fmt.Sprintf("打法 %s 尚未完整落库，拒绝跳过", df.Title)
}
