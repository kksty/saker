package store

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/kksty/saker/internal/model"
)

func manyScopeRefs(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = "in_" + strings.Repeat("x", i%5) + string(rune('a'+i%26))
	}
	return out
}

func countScopeLines(t *testing.T, dir string) int {
	t.Helper()
	raw, err := os.ReadFile(OperatorScopePath(dir))
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	return strings.Count(string(raw), "\n")
}

// Append then read round-trips the documented JSON contract: the exact field
// names, an os- id, an increasing recorded time, and arrays (not null) for the
// two reference lists.
func TestOperatorScopeAppendAndRead(t *testing.T) {
	dir := t.TempDir()
	first, created, err := AppendOperatorScope(dir, OperatorScopeEntry{
		Kind:    OperatorScopeKindAuthorization,
		Summary: "允许对新增资产 A 做只读枚举",
		Note:    "操作员口述",
	})
	if err != nil || !created {
		t.Fatalf("append = %v created=%v", err, created)
	}
	if !strings.HasPrefix(first.ID, "os-") {
		t.Fatalf("generated id = %q, want os- prefix", first.ID)
	}
	if first.Actor != "operator" || first.V != OperatorScopeVersion {
		t.Fatalf("normalized entry = %+v", first)
	}
	second, created, err := AppendOperatorScope(dir, OperatorScopeEntry{
		Kind:       OperatorScopeKindStop,
		Summary:    "停测资产 A",
		IntentIDs:  []string{"in_1", "in_1", "in_2"},
		EntityRefs: []string{"host:10.0.0.9"},
	})
	if err != nil || !created {
		t.Fatalf("append stop = %v created=%v", err, created)
	}
	if len(second.IntentIDs) != 2 || second.IntentIDs[0] != "in_1" || second.IntentIDs[1] != "in_2" {
		t.Fatalf("intent ids not trimmed/deduped in order: %v", second.IntentIDs)
	}
	if !second.At.After(first.At) {
		t.Fatalf("times not strictly increasing: %v then %v", first.At, second.At)
	}

	scope, err := ReadOperatorScope(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(scope.Entries) != 2 || len(scope.BadLines) != 0 || scope.TruncatedTail {
		t.Fatalf("scope = %+v", scope)
	}
	if scope.Entries[0].ID != first.ID || scope.Entries[1].ID != second.ID {
		t.Fatalf("file order = %v,%v", scope.Entries[0].ID, scope.Entries[1].ID)
	}
	if stops := scope.Stops(); len(stops) != 1 || stops[0].ID != second.ID {
		t.Fatalf("stops = %+v", stops)
	}
	// The on-disk line is the documented shape, not a Go struct dump.
	raw, err := os.ReadFile(OperatorScopePath(dir))
	if err != nil {
		t.Fatal(err)
	}
	var line map[string]json.RawMessage
	if err := json.Unmarshal([]byte(strings.SplitN(string(raw), "\n", 2)[0]), &line); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"v", "id", "kind", "at", "actor", "summary", "intent_ids", "entity_refs", "note"} {
		if _, ok := line[key]; !ok {
			t.Fatalf("line missing %q: %s", key, raw)
		}
	}
}

// A project that never recorded a scope has an empty scope, not an error, and a
// missing file is not created by a read.
func TestOperatorScopeMissingFileIsEmptyAndReadOnly(t *testing.T) {
	dir := t.TempDir()
	scope, err := ReadOperatorScope(dir)
	if err != nil {
		t.Fatalf("missing file must be empty, not an error: %v", err)
	}
	if len(scope.Entries) != 0 || scope.TruncatedTail || len(scope.BadLines) != 0 {
		t.Fatalf("scope = %+v", scope)
	}
	if _, err := os.Stat(OperatorScopePath(dir)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read created the file: %v", err)
	}
}

// A torn tail is the crash shape (write without newline): tolerated and flagged.
// A complete but invalid middle line is reported as a bad line and skipped so a
// single corrupt row cannot hide the rest of the operator's record. A read never
// rewrites the file.
func TestOperatorScopeToleratesTailAndReportsBadLines(t *testing.T) {
	dir := t.TempDir()
	path := OperatorScopePath(dir)
	good1 := `{"v":1,"id":"os-a","kind":"authorization","at":"2026-01-02T03:04:05.000000001Z","actor":"operator","summary":"s1","intent_ids":[],"entity_refs":[],"note":""}`
	good2 := `{"v":1,"id":"os-b","kind":"stop","at":"2026-01-02T03:04:06Z","actor":"operator","summary":"s2","intent_ids":["in_1"],"entity_refs":["host:x"],"note":""}`
	content := good1 + "\n" + `{"v":1,"id":"os-broken","kind":"stop"` + "\n" + "\n" + good2 + "\n" + `{"v":1,"id":"os-torn","kind":"sto`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	scope, err := ReadOperatorScope(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(scope.Entries) != 2 || scope.Entries[0].ID != "os-a" || scope.Entries[1].ID != "os-b" {
		t.Fatalf("entries = %+v", scope.Entries)
	}
	if !scope.TruncatedTail {
		t.Fatal("torn tail not flagged")
	}
	if len(scope.BadLines) != 1 || scope.BadLines[0].Line != 2 {
		t.Fatalf("bad lines = %+v, want line 2", scope.BadLines)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("read rewrote the append-only file")
	}
}

// The reader is stricter than json.Unmarshal: a line with the wrong kind, a zero
// time or a bad id is a bad line, not an entry.
func TestOperatorScopeReadRejectsContractViolations(t *testing.T) {
	dir := t.TempDir()
	lines := []string{
		`{"v":2,"id":"os-1","kind":"stop","at":"2026-01-02T03:04:06Z","actor":"operator","summary":"s","intent_ids":[],"entity_refs":[],"note":""}`,
		`{"v":1,"id":"bad","kind":"stop","at":"2026-01-02T03:04:06Z","actor":"operator","summary":"s","intent_ids":[],"entity_refs":[],"note":""}`,
		`{"v":1,"id":"os-2","kind":"other","at":"2026-01-02T03:04:06Z","actor":"operator","summary":"s","intent_ids":[],"entity_refs":[],"note":""}`,
		`{"v":1,"id":"os-3","kind":"stop","at":"0001-01-01T00:00:00Z","actor":"operator","summary":"s","intent_ids":[],"entity_refs":[],"note":""}`,
		`{"v":1,"id":"os-4","kind":"stop","at":"2026-01-02T03:04:06Z","actor":"operator","summary":"","intent_ids":[],"entity_refs":[],"note":""}`,
	}
	if err := os.WriteFile(OperatorScopePath(dir), []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	scope, err := ReadOperatorScope(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(scope.Entries) != 0 {
		t.Fatalf("accepted invalid entries: %+v", scope.Entries)
	}
	if len(scope.BadLines) != len(lines) {
		t.Fatalf("bad lines = %d, want %d: %+v", len(scope.BadLines), len(lines), scope.BadLines)
	}
}

// A crash can leave a partial last line. The next append must start on a fresh
// line so the committed decision stays parseable; the fragment becomes one
// reported bad line, never a corrupted new entry.
func TestAppendOperatorScopeAfterTornTail(t *testing.T) {
	dir := t.TempDir()
	path := OperatorScopePath(dir)
	good := `{"v":1,"id":"os-a","kind":"authorization","at":"2026-01-02T03:04:05Z","actor":"operator","summary":"s1","intent_ids":[],"entity_refs":[],"note":""}`
	if err := os.WriteFile(path, []byte(good+"\n"+`{"v":1,"id":"os-tor`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, created, err := AppendOperatorScope(dir, OperatorScopeEntry{Kind: OperatorScopeKindStop, Summary: "停测 A"}); err != nil || !created {
		t.Fatalf("append after torn tail = %v created=%v", err, created)
	}
	scope, err := ReadOperatorScope(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(scope.Entries) != 2 || scope.Entries[0].ID != "os-a" || scope.Entries[1].Kind != OperatorScopeKindStop {
		t.Fatalf("entries after torn-tail append = %+v", scope.Entries)
	}
	if scope.TruncatedTail {
		t.Fatal("appended file still reports a torn tail")
	}
	if len(scope.BadLines) != 1 {
		t.Fatalf("bad lines = %+v, want the old fragment reported", scope.BadLines)
	}
}

// The append path shares the same contract and rejects bad input before touching
// the file.
func TestAppendOperatorScopeValidates(t *testing.T) {
	cases := []struct {
		name  string
		entry OperatorScopeEntry
	}{
		{"bad kind", OperatorScopeEntry{Kind: "delete", Summary: "s"}},
		{"empty summary", OperatorScopeEntry{Kind: OperatorScopeKindStop, Summary: "  "}},
		{"too long summary", OperatorScopeEntry{Kind: OperatorScopeKindStop, Summary: strings.Repeat("字", MaxOperatorScopeSummaryRunes+1)}},
		{"too long note", OperatorScopeEntry{Kind: OperatorScopeKindStop, Summary: "s", Note: strings.Repeat("n", MaxOperatorScopeNoteRunes+1)}},
		{"too many intents", OperatorScopeEntry{Kind: OperatorScopeKindStop, Summary: "s", IntentIDs: manyScopeRefs(MaxOperatorScopeIntentIDs + 1)}},
		{"bad actor", OperatorScopeEntry{Kind: OperatorScopeKindStop, Summary: "s", Actor: "commander"}},
		{"bad id", OperatorScopeEntry{Kind: OperatorScopeKindStop, Summary: "s", ID: "os/../x"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			_, _, err := AppendOperatorScope(dir, tc.entry)
			if !errors.Is(err, ErrOperatorScopeInvalid) {
				t.Fatalf("err = %v, want ErrOperatorScopeInvalid", err)
			}
			if _, statErr := os.Stat(OperatorScopePath(dir)); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("invalid append created the file")
			}
		})
	}
}

// Replaying the same decision (same explicit id / same idempotency-derived id)
// must not append a second row; a same-id different-payload request is a
// conflict. This is what makes a double-clicked stop safe.
func TestAppendOperatorScopeIdempotent(t *testing.T) {
	dir := t.TempDir()
	first, created, err := AppendOperatorScope(dir, OperatorScopeEntry{
		ID: "os-fixed", Kind: OperatorScopeKindStop, Summary: "停测 A",
		IntentIDs: []string{"in_a"}, EntityRefs: []string{"host:a"},
	})
	if err != nil || !created {
		t.Fatalf("append = %v created=%v", err, created)
	}
	replay, created, err := AppendOperatorScope(dir, OperatorScopeEntry{
		ID: "os-fixed", Kind: OperatorScopeKindStop, Summary: "停测 A",
		IntentIDs: []string{"in_a"}, EntityRefs: []string{"host:a"},
	})
	if err != nil || created {
		t.Fatalf("replay = %v created=%v, want replay without new line", err, created)
	}
	if replay.ID != first.ID || !replay.At.Equal(first.At) {
		t.Fatalf("replay returned a different row: first=%+v replay=%+v", first, replay)
	}
	if n := countScopeLines(t, dir); n != 1 {
		t.Fatalf("file has %d lines, want 1", n)
	}
	if _, _, err := AppendOperatorScope(dir, OperatorScopeEntry{
		ID: "os-fixed", Kind: OperatorScopeKindStop, Summary: "停测 B",
	}); !errors.Is(err, ErrOperatorScopeConflict) {
		t.Fatalf("same id different payload = %v, want conflict", err)
	}
	if n := countScopeLines(t, dir); n != 1 {
		t.Fatalf("conflict appended: %d lines", n)
	}
}

// The recorded times are strictly increasing even if the caller supplies a time
// that is not (clock step / concurrent requests). Order is the file's contract.
func TestAppendOperatorScopeKeepsTimeOrder(t *testing.T) {
	dir := t.TempDir()
	past := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	first, _, err := AppendOperatorScope(dir, OperatorScopeEntry{Kind: OperatorScopeKindAuthorization, Summary: "s1", At: past})
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := AppendOperatorScope(dir, OperatorScopeEntry{Kind: OperatorScopeKindAuthorization, Summary: "s2", At: past})
	if err != nil {
		t.Fatal(err)
	}
	if !second.At.After(first.At) {
		t.Fatalf("second at %v not after first %v", second.At, first.At)
	}
	scope, _ := ReadOperatorScope(dir)
	if len(scope.Entries) != 2 || !scope.Entries[1].At.After(scope.Entries[0].At) {
		t.Fatalf("file order broken: %+v", scope.Entries)
	}
}

// The late-fact rule is identity + time only: the exact entity_ref list and the
// fact's recorded creation time, never the title/summary.
func TestOperatorScopeMatchesLateFact(t *testing.T) {
	stopAt := time.Date(2026, 2, 1, 10, 0, 0, 0, time.UTC)
	stop := OperatorScopeEntry{Kind: OperatorScopeKindStop, At: stopAt, EntityRefs: []string{"host:10.0.0.9"}}
	after := model.Fact{ID: "f_after", EntityRef: "host:10.0.0.9", Title: "observed service", CreatedAt: stopAt.Add(time.Minute)}
	before := model.Fact{ID: "f_before", EntityRef: "host:10.0.0.9", CreatedAt: stopAt.Add(-time.Minute)}
	other := model.Fact{ID: "f_other", EntityRef: "host:10.0.0.10", CreatedAt: stopAt.Add(time.Minute)}
	noRef := model.Fact{ID: "f_noref", Title: "host:10.0.0.9 looks interesting", CreatedAt: stopAt.Add(time.Minute)}
	auth := OperatorScopeEntry{Kind: OperatorScopeKindAuthorization, At: stopAt, EntityRefs: []string{"host:10.0.0.9"}}

	if !stop.MatchesLateFact(after) {
		t.Fatal("fact after the stop for a listed entity must match")
	}
	if stop.MatchesLateFact(before) {
		t.Fatal("fact before the stop must not match")
	}
	if stop.MatchesLateFact(other) {
		t.Fatal("unlisted entity must not match")
	}
	if stop.MatchesLateFact(noRef) {
		t.Fatal("empty entity_ref must not be inferred from the title")
	}
	if auth.MatchesLateFact(after) {
		t.Fatal("authorization entries must not match as stops")
	}
}
