package store

// Operator scope is the operator's append-only record of two decisions that do
// not belong to the ledger: an authorisation and a stop. The ledger's object
// model is deliberately fixed (no schema migration this cycle), and these rows
// are not facts, not instructions and not project settings: they are the
// operator's own scope statements, kept next to the project so a later read-only
// metric (P0-5) can count them without opening the ledger.
//
// File: <projectDir>/operator-scope.jsonl, one JSON object per line:
//
//	{"v":1,"id":"os-…","kind":"authorization"|"stop","at":"RFC3339Nano",
//	 "actor":"operator","summary":"…","intent_ids":[…],"entity_refs":[…],"note":"…"}
//
// The file is appended with O_APPEND and fsynced. It is never rewritten,
// compacted or repaired: a reader tolerates a torn tail (a crash between write
// and newline) and reports a malformed middle line explicitly as a bad line
// instead of silently dropping it. Nothing outside the operator API writes it;
// the same account can still edit the file by hand, which is a documented
// limitation (there is no per-request enforcement) and not something a reader
// can detect.

import (
	"bufio"
	"crypto/rand"
	"encoding/base32"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/kksty/saker/internal/model"
)

// OperatorScopeFile is the bypass file's name inside a project directory.
const OperatorScopeFile = "operator-scope.jsonl"

// OperatorScopeVersion is the only line format this build writes and accepts.
const OperatorScopeVersion = 1

// The two recorded kinds. An authorisation widens what the operator has allowed;
// a stop narrows it. They share one file because they answer the same question
// ("what did the operator decide about scope, and when") and because one
// append-only stream keeps their relative order intact.
const (
	OperatorScopeKindAuthorization = "authorization"
	OperatorScopeKindStop          = "stop"
)

// Field bounds. They are enforced on append and re-checked on read so a hand
// edit cannot smuggle an unbounded row into a read-only metric. The bounds are
// intentionally generous: this is the operator's own prose, not a prompt.
const (
	MaxOperatorScopeSummaryRunes = 2000
	MaxOperatorScopeNoteRunes    = 4000
	MaxOperatorScopeIntentIDs    = 64
	MaxOperatorScopeEntityRefs   = 64
	MaxOperatorScopeRefRunes     = 256
	MaxOperatorScopeIDRunes      = 80
)

var (
	// ErrOperatorScopeInvalid means the entry cannot be represented in the file
	// contract (bad kind, empty summary, an over-long field, ...).
	ErrOperatorScopeInvalid = errors.New("操作员范围条目无效")
	// ErrOperatorScopeConflict means the same entry id was reused with a
	// different payload. The first row is immutable; a retry must be identical.
	ErrOperatorScopeConflict = errors.New("同一个操作员范围条目 id 对应不同内容")
)

// OperatorScopeEntry is one line of operator-scope.jsonl. At is the recorded
// decision time; the reader never re-derives it from the file's mtime.
type OperatorScopeEntry struct {
	V          int       `json:"v"`
	ID         string    `json:"id"`
	Kind       string    `json:"kind"`
	At         time.Time `json:"at"`
	Actor      string    `json:"actor"`
	Summary    string    `json:"summary"`
	IntentIDs  []string  `json:"intent_ids"`
	EntityRefs []string  `json:"entity_refs"`
	Note       string    `json:"note"`
}

// OperatorScopeBadLine is one complete line that could not be parsed or failed
// the entry contract. It is reported, never repaired: the caller decides whether
// a bad line invalidates the read (the API surfaces the count; P0-5 metrics can
// refuse to report from a file that reports bad lines).
type OperatorScopeBadLine struct {
	Line  int    `json:"line"` // 1-based physical line number
	Error string `json:"error"`
}

// OperatorScope is one read of the file. Missing file yields an empty value and
// no error: "no operator scope recorded" is a normal project state, not a
// failure.
type OperatorScope struct {
	Entries []OperatorScopeEntry `json:"entries"`
	// BadLines counts complete-but-invalid middle lines. They are skipped so one
	// corrupt row cannot hide the rest of the operator's record.
	BadLines []OperatorScopeBadLine `json:"bad_lines,omitempty"`
	// TruncatedTail is true when the file ends without a newline. That is the
	// expected shape of a crash between write and newline; the partial line is
	// ignored (it was never a committed decision) and not counted as a bad line.
	TruncatedTail bool `json:"truncated_tail,omitempty"`
}

// Stops returns the stop entries in file order. It is the projection P0-5's
// "facts after a stop" metric reads, so the filter lives here once.
func (o OperatorScope) Stops() []OperatorScopeEntry {
	out := make([]OperatorScopeEntry, 0, len(o.Entries))
	for _, e := range o.Entries {
		if e.Kind == OperatorScopeKindStop {
			out = append(out, e)
		}
	}
	return out
}

// OperatorScopePath is the file location for a project directory. It is exported
// so P0-5 (and tests) reopen exactly the same path the API writes.
func OperatorScopePath(projectDir string) string {
	return filepath.Join(projectDir, OperatorScopeFile)
}

// MatchesLateFact reports whether f is a fact recorded after this stop for an
// entity the operator explicitly listed. It is a pure identity+time rule: the
// exact entity_ref string and the recorded creation time decide, never the
// fact's title, summary or kind. Entries whose kind is not a stop never match.
func (e OperatorScopeEntry) MatchesLateFact(f model.Fact) bool {
	if e.Kind != OperatorScopeKindStop || f.EntityRef == "" || !f.CreatedAt.After(e.At) {
		return false
	}
	for _, ref := range e.EntityRefs {
		if ref == f.EntityRef {
			return true
		}
	}
	return false
}

// OperatorScopeAppendMu serialises same-process appends: two HTTP requests
// arriving together must not both read "no duplicate" and then both append. It
// is process-local, like the API that writes the file; a second process editing
// the same path is outside this cycle's contract (same-account bypass).
var operatorScopeAppendMu sync.Mutex

// AppendOperatorScope validates one entry and appends it to
// <projectDir>/operator-scope.jsonl. It returns the stored entry (with a
// generated id and a strictly increasing time) and whether a new line was
// written.
//
// Idempotency: if an entry with the same id already exists and its payload is
// identical (kind/actor/summary/intent_ids/entity_refs/note; the recorded time
// is deliberately not part of the comparison), the existing entry is returned
// with created=false. The file is append-only, so a retry must never add a
// second row for the same decision. A same id with a different payload is an
// explicit conflict.
func AppendOperatorScope(projectDir string, entry OperatorScopeEntry) (OperatorScopeEntry, bool, error) {
	operatorScopeAppendMu.Lock()
	defer operatorScopeAppendMu.Unlock()
	return appendOperatorScopeLocked(projectDir, entry)
}

func appendOperatorScopeLocked(projectDir string, entry OperatorScopeEntry) (OperatorScopeEntry, bool, error) {
	if strings.TrimSpace(projectDir) == "" {
		return OperatorScopeEntry{}, false, fmt.Errorf("%w：项目目录为空", ErrOperatorScopeInvalid)
	}
	normalized, err := normalizeOperatorScopeEntry(entry)
	if err != nil {
		return OperatorScopeEntry{}, false, err
	}
	existing, err := ReadOperatorScope(projectDir)
	if err != nil {
		return OperatorScopeEntry{}, false, err
	}
	for _, old := range existing.Entries {
		if old.ID != normalized.ID {
			continue
		}
		if sameOperatorScopePayload(old, normalized) {
			return old, false, nil
		}
		return OperatorScopeEntry{}, false, fmt.Errorf("%w：%s", ErrOperatorScopeConflict, normalized.ID)
	}
	// Strictly increasing recorded times make the file a canonical order even
	// when the wall clock steps backwards or two requests share a clock tick.
	// A caller-supplied time in the past is normalised forward rather than
	// rejected: the file's purpose is the operator's decision order.
	if n := len(existing.Entries); n > 0 && !existing.Entries[n-1].At.Before(normalized.At) {
		normalized.At = existing.Entries[n-1].At.Add(time.Nanosecond)
	}
	line, err := json.Marshal(normalized)
	if err != nil {
		return OperatorScopeEntry{}, false, err
	}
	// A torn tail means the previous writer died between write and newline. The
	// append-only file is never rewritten, so start our line on a fresh line:
	// the fragment becomes one invalid middle line (reported by the next read)
	// and this committed decision stays parseable.
	prefix := []byte{}
	if existing.TruncatedTail {
		prefix = []byte("\n")
	}
	line = append(line, '\n')
	path := OperatorScopePath(projectDir)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return OperatorScopeEntry{}, false, fmt.Errorf("打开操作员范围文件失败：%w", err)
	}
	if _, err := f.Write(append(prefix, line...)); err != nil {
		_ = f.Close()
		return OperatorScopeEntry{}, false, fmt.Errorf("写操作员范围文件失败：%w", err)
	}
	// Durability before success: the caller may cancel runs on the strength of
	// this row, so a power loss must not lose the stop while the cancellation
	// survives.
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return OperatorScopeEntry{}, false, fmt.Errorf("同步操作员范围文件失败：%w", err)
	}
	if err := f.Close(); err != nil {
		return OperatorScopeEntry{}, false, err
	}
	return normalized, true, nil
}

// ReadOperatorScope parses the whole file. It never writes and never repairs.
//
// Error policy (also exercised by operator_scope_test.go):
//   - missing file        -> empty scope, nil error;
//   - torn final line     -> TruncatedTail=true, partial line ignored;
//   - invalid middle line -> one BadLines record, remaining lines still read;
//   - blank lines         -> ignored (plain whitespace is not a record);
//   - I/O failure         -> error, because the caller cannot tell a short read
//     from a clean one.
func ReadOperatorScope(projectDir string) (OperatorScope, error) {
	out := OperatorScope{Entries: []OperatorScopeEntry{}}
	path := OperatorScopePath(projectDir)
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return out, nil
		}
		return out, fmt.Errorf("读取操作员范围文件失败：%w", err)
	}
	defer f.Close()
	r := bufio.NewReader(f)
	lineNo := 0
	for {
		line, readErr := r.ReadString('\n')
		if len(line) > 0 {
			complete := strings.HasSuffix(line, "\n")
			lineNo++
			content := strings.TrimRight(line, "\r\n")
			switch {
			case strings.TrimSpace(content) == "":
				// Blank lines are layout, not records.
			case !complete:
				// A torn tail is the expected crash shape: the decision was
				// never committed with a newline, so it is ignored and flagged.
				out.TruncatedTail = true
			default:
				entry, perr := parseOperatorScopeLine(content)
				if perr != nil {
					out.BadLines = append(out.BadLines, OperatorScopeBadLine{Line: lineNo, Error: perr.Error()})
				} else {
					out.Entries = append(out.Entries, entry)
				}
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			return out, fmt.Errorf("读取操作员范围文件失败：%w", readErr)
		}
		if !strings.HasSuffix(line, "\n") {
			break
		}
	}
	return out, nil
}

func parseOperatorScopeLine(line string) (OperatorScopeEntry, error) {
	var entry OperatorScopeEntry
	dec := json.NewDecoder(strings.NewReader(line))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&entry); err != nil {
		return OperatorScopeEntry{}, fmt.Errorf("不是合法的范围条目 JSON：%v", err)
	}
	if err := validateOperatorScopeEntry(entry); err != nil {
		return OperatorScopeEntry{}, err
	}
	entry = normalizeOperatorScopeRefs(entry)
	return entry, nil
}

// normalizeOperatorScopeEntry applies the shared validation and fills the
// writer-side defaults (v, actor, generated id, empty arrays).
func normalizeOperatorScopeEntry(entry OperatorScopeEntry) (OperatorScopeEntry, error) {
	entry.Summary = strings.TrimSpace(entry.Summary)
	entry.Note = strings.TrimSpace(entry.Note)
	if entry.V == 0 {
		entry.V = OperatorScopeVersion
	}
	if entry.At.IsZero() {
		// Writer-side default: the API records "now" at the decision point; a
		// zero time here would break the file's canonical order.
		entry.At = time.Now()
	}
	if strings.TrimSpace(entry.Actor) == "" {
		entry.Actor = "operator"
	} else {
		entry.Actor = strings.TrimSpace(entry.Actor)
	}
	if entry.Actor != "operator" {
		return OperatorScopeEntry{}, fmt.Errorf("%w：actor 只能是 operator", ErrOperatorScopeInvalid)
	}
	if strings.TrimSpace(entry.ID) == "" {
		id, err := newOperatorScopeID()
		if err != nil {
			return OperatorScopeEntry{}, err
		}
		entry.ID = id
	}
	entry = normalizeOperatorScopeRefs(entry)
	if err := validateOperatorScopeEntry(entry); err != nil {
		return OperatorScopeEntry{}, err
	}
	return entry, nil
}

// normalizeOperatorScopeRefs trims, drops empties and de-duplicates explicit
// references. Order is preserved: the operator's list is a statement, not a set
// the program may reorder.
func normalizeOperatorScopeRefs(entry OperatorScopeEntry) OperatorScopeEntry {
	entry.IntentIDs = normalizeOperatorScopeList(entry.IntentIDs)
	entry.EntityRefs = normalizeOperatorScopeList(entry.EntityRefs)
	return entry
}

func normalizeOperatorScopeList(in []string) []string {
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, v := range in {
		v = strings.TrimSpace(v)
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}

func validateOperatorScopeEntry(entry OperatorScopeEntry) error {
	if entry.V != OperatorScopeVersion {
		return fmt.Errorf("%w：v 只能是 %d", ErrOperatorScopeInvalid, OperatorScopeVersion)
	}
	if err := validateOperatorScopeID(entry.ID); err != nil {
		return err
	}
	switch entry.Kind {
	case OperatorScopeKindAuthorization, OperatorScopeKindStop:
	default:
		return fmt.Errorf("%w：kind 只能是 authorization 或 stop", ErrOperatorScopeInvalid)
	}
	if entry.At.IsZero() {
		return fmt.Errorf("%w：at 缺失", ErrOperatorScopeInvalid)
	}
	if entry.Actor == "" {
		return fmt.Errorf("%w：actor 缺失", ErrOperatorScopeInvalid)
	}
	if entry.Summary == "" {
		return fmt.Errorf("%w：summary 不能为空", ErrOperatorScopeInvalid)
	}
	if n := len([]rune(entry.Summary)); n > MaxOperatorScopeSummaryRunes {
		return fmt.Errorf("%w：summary 最多 %d 字（当前 %d）", ErrOperatorScopeInvalid, MaxOperatorScopeSummaryRunes, n)
	}
	if n := len([]rune(entry.Note)); n > MaxOperatorScopeNoteRunes {
		return fmt.Errorf("%w：note 最多 %d 字（当前 %d）", ErrOperatorScopeInvalid, MaxOperatorScopeNoteRunes, n)
	}
	if len(entry.IntentIDs) > MaxOperatorScopeIntentIDs {
		return fmt.Errorf("%w：intent_ids 最多 %d 项", ErrOperatorScopeInvalid, MaxOperatorScopeIntentIDs)
	}
	if len(entry.EntityRefs) > MaxOperatorScopeEntityRefs {
		return fmt.Errorf("%w：entity_refs 最多 %d 项", ErrOperatorScopeInvalid, MaxOperatorScopeEntityRefs)
	}
	for _, ref := range entry.IntentIDs {
		if ref == "" || len([]rune(ref)) > MaxOperatorScopeRefRunes {
			return fmt.Errorf("%w：intent_ids 项应为 1–%d 字", ErrOperatorScopeInvalid, MaxOperatorScopeRefRunes)
		}
	}
	for _, ref := range entry.EntityRefs {
		if ref == "" || len([]rune(ref)) > MaxOperatorScopeRefRunes {
			return fmt.Errorf("%w：entity_refs 项应为 1–%d 字", ErrOperatorScopeInvalid, MaxOperatorScopeRefRunes)
		}
	}
	return nil
}

func validateOperatorScopeID(id string) error {
	if !strings.HasPrefix(id, "os-") || len(id) <= len("os-") || len(id) > MaxOperatorScopeIDRunes {
		return fmt.Errorf("%w：id 必须是 os- 前缀的稳定标识", ErrOperatorScopeInvalid)
	}
	for _, r := range id[len("os-"):] {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			continue
		}
		return fmt.Errorf("%w：id 含非法字符", ErrOperatorScopeInvalid)
	}
	return nil
}

// sameOperatorScopePayload compares the operator-controlled content. The time is
// excluded so an idempotent retry (which computes a new now()) still matches the
// row it already wrote.
func sameOperatorScopePayload(a, b OperatorScopeEntry) bool {
	return a.Kind == b.Kind && a.Actor == b.Actor && a.Summary == b.Summary &&
		slices.Equal(a.IntentIDs, b.IntentIDs) && slices.Equal(a.EntityRefs, b.EntityRefs) && a.Note == b.Note
}

func newOperatorScopeID() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("操作员范围条目 id 熵不可用：%w", err)
	}
	code := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b[:])[:10]
	return "os-" + strings.ToLower(code), nil
}
