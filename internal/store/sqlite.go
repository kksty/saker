package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"github.com/kksty/saker/internal/model"
	_ "modernc.org/sqlite"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type sqliteBackend struct{ db *sql.DB }
type sqliteMeta struct {
	Version    int     `json:"version"`
	MessageSeq uint64  `json:"message_seq"`
	Memory     *Memory `json:"memory"`
}

// OpenSQLite opens the canonical ledger, not a second writable index beside a
// JSON ledger. Only the current SQLite format is supported.
//
// It keeps no idle connection: the handle is released as soon as an operation
// returns, so a caller may remove the directory afterwards without closing
// first. That is the right default for one-shot tools and for fixtures.
// Long-lived project runtimes use OpenSQLitePooled instead.
func OpenSQLite(path string) (*Store, error) { return openSQLite(path, false) }

// OpenSQLitePooled opens the canonical ledger and keeps its one connection idle
// between operations. Opening a fresh SQLite handle per write costs more than
// the write itself (measured on Windows 2026-11-23: p50 13.55ms per ledger
// commit transiently, 1.57ms pooled), and every project write pays it. The
// price is one idle OS handle: a caller that removes a project directory must
// Close the store first, because Windows refuses to delete a directory whose
// ledger/WAL files are still open. Project runtimes take this mode and close on
// delete (cmd/saker/hub.go deleteProject); nothing may rely on the garbage
// collector to release the file.
func OpenSQLitePooled(path string) (*Store, error) { return openSQLite(path, true) }

func openSQLite(path string, pooled bool) (*Store, error) {
	info, statErr := os.Stat(path)
	fresh := os.IsNotExist(statErr)
	if statErr != nil && !fresh {
		return nil, statErr
	}
	if !fresh && info.Size() == 0 {
		return nil, fmt.Errorf("empty SQLite ledger: %s", path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", sqliteURL(path, false))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if pooled {
		db.SetMaxIdleConns(1)
	} else {
		// Transactions keep one connection; an idle handle is not retained.
		db.SetMaxIdleConns(0)
	}
	fail := func(e error) (*Store, error) { _ = db.Close(); return nil, e }
	if fresh {
		for _, stmt := range []string{
			`CREATE TABLE meta (id INTEGER PRIMARY KEY CHECK(id=1), payload BLOB NOT NULL)`,
			`CREATE TABLE objects (kind TEXT NOT NULL,id TEXT NOT NULL,position INTEGER NOT NULL,version INTEGER NOT NULL,seq INTEGER NOT NULL,payload BLOB NOT NULL,PRIMARY KEY(kind,id))`,
			`CREATE INDEX objects_order ON objects(kind,position,id)`,
			`CREATE TABLE versions (seq INTEGER PRIMARY KEY,kind TEXT NOT NULL,id TEXT NOT NULL,version INTEGER NOT NULL,payload BLOB NOT NULL)`,
			`CREATE INDEX versions_object ON versions(kind,id,seq)`,
			`CREATE VIRTUAL TABLE fact_search USING fts5(id UNINDEXED,title,summary,doc,tokenize='trigram')`,
			`CREATE TABLE vectors (seq INTEGER PRIMARY KEY AUTOINCREMENT,model TEXT NOT NULL,hash TEXT NOT NULL,vector BLOB NOT NULL,UNIQUE(model,hash))`,
			`CREATE TABLE relations (owner TEXT NOT NULL,source TEXT NOT NULL,target TEXT NOT NULL,relation TEXT NOT NULL,PRIMARY KEY(owner,source,target,relation))`,
			`CREATE INDEX relations_source ON relations(source)`, `CREATE INDEX relations_target ON relations(target)`,
			`PRAGMA user_version=1`,
		} {
			if _, err = db.Exec(stmt); err != nil {
				return fail(err)
			}
		}
		mem := &Memory{Epoch: NewID("ledger"), Checkpoints: map[string]Checkpoint{}}
		mem.ContentHash = contentHash(nil)
		raw, _ := json.Marshal(sqliteMeta{schemaVersion, 0, mem})
		if _, err = db.Exec("INSERT INTO meta VALUES(1,?)", raw); err != nil {
			return fail(err)
		}
	}
	var format int
	if err = db.QueryRow("PRAGMA user_version").Scan(&format); err != nil || format != 1 {
		return fail(fmt.Errorf("unsupported SQLite ledger format %d: %v", format, err))
	}
	s := &Store{path: path, sqlite: &sqliteBackend{db: db}}
	if err = s.loadSQLite(); err != nil {
		return fail(err)
	}
	return s, nil
}

func (s *Store) loadSQLite() error {
	db, err := readSQLiteDB(s.sqlite.db)
	if err != nil {
		return err
	}
	s.db = db
	s.readEpoch = db.Memory.Epoch
	s.readCacheMu.Lock()
	s.readCache, s.readCacheOrder, s.readCacheBytes = nil, nil, 0
	s.readCacheMu.Unlock()
	s.initRecords()
	if err := s.validateMemory(); err != nil {
		return err
	}
	s.committedMeta = sqliteMeta{db.Version, db.MessageSeq, db.Memory}
	return nil
}

// readSQLiteDB loads one committed ledger from an open connection. It is shared
// by the live store and the read-only inspector so both agree on what a valid
// ledger is: schema check, object rows and version history.
func readSQLiteDB(sqlDB *sql.DB) (DB, error) {
	var raw []byte
	if err := sqlDB.QueryRow("SELECT payload FROM meta WHERE id=1").Scan(&raw); err != nil {
		return DB{}, err
	}
	var meta sqliteMeta
	if err := json.Unmarshal(raw, &meta); err != nil {
		return DB{}, err
	}
	if meta.Version < schemaMinReadable || meta.Version > schemaVersion || meta.Memory == nil {
		return DB{}, fmt.Errorf("unsupported ledger schema %d (expected %d-%d; use a new data directory; migration is not supported)", meta.Version, schemaMinReadable, schemaVersion)
	}
	db := DB{Version: meta.Version, MessageSeq: meta.MessageSeq, Memory: meta.Memory}
	rows, err := sqlDB.Query("SELECT kind,payload FROM objects ORDER BY kind,position,id")
	if err != nil {
		return DB{}, err
	}
	for rows.Next() {
		var kind string
		var value []byte
		if err = rows.Scan(&kind, &value); err != nil {
			break
		}
		if err = appendRow(&db, kind, value); err != nil {
			break
		}
	}
	rowErr := rows.Err()
	_ = rows.Close()
	if err != nil {
		return DB{}, err
	}
	if rowErr != nil {
		return DB{}, rowErr
	}
	rows, err = sqlDB.Query("SELECT payload FROM versions ORDER BY seq")
	if err != nil {
		return DB{}, err
	}
	for rows.Next() {
		var value []byte
		if err = rows.Scan(&value); err != nil {
			break
		}
		var c Change
		if err = json.Unmarshal(value, &c); err != nil {
			break
		}
		db.Memory.History = append(db.Memory.History, c)
	}
	rowErr = rows.Err()
	_ = rows.Close()
	if err != nil {
		return DB{}, err
	}
	if rowErr != nil {
		return DB{}, rowErr
	}
	return db, nil
}

// ReadSnapshot opens an offline ledger read-only and returns its committed
// state. It never creates, migrates or checkpoints the file: import validation
// must be able to inspect bytes it has not accepted yet, and a mismatch should
// leave no WAL behind in the staging directory.
func ReadSnapshot(path string) (DB, error) {
	info, err := os.Stat(path)
	if err != nil {
		return DB{}, err
	}
	if info.IsDir() || info.Size() == 0 {
		return DB{}, fmt.Errorf("empty SQLite ledger: %s", path)
	}
	db, err := sql.Open("sqlite", sqliteURL(path, true))
	if err != nil {
		return DB{}, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	var format int
	if err := db.QueryRow("PRAGMA user_version").Scan(&format); err != nil {
		return DB{}, err
	}
	if format != 1 {
		return DB{}, fmt.Errorf("unsupported SQLite ledger format %d", format)
	}
	snap, err := readSQLiteDB(db)
	if err != nil {
		return DB{}, err
	}
	// The same invariant the live open enforces. An import must not publish a
	// ledger that the very next process would refuse to load.
	probe := &Store{db: snap}
	probe.initRecords()
	if err := probe.validateMemory(); err != nil {
		return DB{}, err
	}
	return snap, nil
}

// BackupTo writes a consistent standalone copy of the committed ledger,
// including rows still living in the WAL. A file copy of a live SQLite database
// can silently omit committed data; VACUUM INTO is the supported snapshot
// primitive. The backup has no WAL and can be opened read-only by the receiver.
func (s *Store) BackupTo(path string) error {
	if s == nil || s.sqlite == nil {
		return fmt.Errorf("ledger is not open")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	// VACUUM INTO refuses to overwrite; remove a stale partial from an earlier
	// failed attempt first. The caller owns the destination.
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	// Hold the store's read lock: writers take the write lock around every
	// flush, so no transaction can commit between the snapshot and the file
	// walk that consumes it.
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, err := s.sqlite.db.Exec("VACUUM INTO ?", path); err != nil {
		return fmt.Errorf("snapshot ledger: %w", err)
	}
	return os.Chmod(path, 0600)
}

// sqliteURL builds the file: DSN. write=true enables WAL and full synchronous;
// read-only uses mode=ro so inspecting an untrusted or already-closed ledger
// cannot create journal files next to it.
func sqliteURL(path string, readOnly bool) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	p := filepath.ToSlash(abs)
	if filepath.VolumeName(abs) != "" {
		p = "/" + p
	}
	u := url.URL{Scheme: "file", Path: p}
	params := url.Values{}
	params.Add("_pragma", "busy_timeout(5000)")
	if readOnly {
		params.Set("mode", "ro")
	} else {
		params.Add("_pragma", "journal_mode(WAL)")
		params.Add("_pragma", "synchronous(FULL)")
	}
	u.RawQuery = params.Encode()
	return u.String()
}

func (s *Store) flushSQLite(kinds ...string) (err error) {
	if s.records == nil {
		s.initRecords()
	}
	previousRev := s.committedRev
	defer func() {
		if err != nil {
			s.rev = previousRev
			if restoreErr := s.restoreCommittedRows(); restoreErr != nil {
				err = fmt.Errorf("%w; restore committed ledger: %v", err, restoreErr)
			}
		}
	}()
	memory, next, changes := s.prepareMemory(kinds...)
	tx, err := s.sqlite.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, c := range changes {
		if err = writeSQLiteRow(tx, c); err != nil {
			return err
		}
	}
	mem := *memory
	mem.History = nil
	// The stamp says what the file may contain, not who opened it. A proposal is
	// the only row the older layout cannot place, so the first commit that writes
	// one raises the stamp in this same transaction: an older build then refuses
	// the file as "schema 14" instead of tripping over an unknown row, and a
	// project that never wrote one keeps its 13 stamp and stays readable by both.
	// It never goes back down: lowering it while a proposal row is inside would
	// promise a file the older build cannot read.
	version := s.db.Version
	if len(s.db.Proposals) > 0 && version < schemaVersion {
		version = schemaVersion
	}
	raw, err := json.Marshal(sqliteMeta{version, s.db.MessageSeq, &mem})
	if err != nil {
		return err
	}
	if _, err = tx.Exec("UPDATE meta SET payload=? WHERE id=1", raw); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	s.db.Version = version
	s.db.Memory = memory
	for _, r := range next {
		s.putRecord(r)
	}
	for _, c := range changes {
		if c.Operation == "delete" {
			key := recordKey(c.Kind, c.ID)
			delete(s.records, key)
			delete(s.recordKinds[c.Kind], key)
		}
	}
	s.committedRev = s.rev
	s.committedMeta = sqliteMeta{s.db.Version, s.db.MessageSeq, memory}
	return nil
}

// A failing connection cannot be relied on to reload its own rollback.
// Every record in this index still belongs to the last successful commit.
func (s *Store) restoreCommittedRows() error {
	meta := s.committedMeta
	db := DB{Version: meta.Version, MessageSeq: meta.MessageSeq, Memory: meta.Memory}
	rows := make([]record, 0, len(s.records))
	for _, row := range s.records {
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if a.kind != b.kind {
			return a.kind < b.kind
		}
		if a.position != b.position {
			return a.position < b.position
		}
		return a.id < b.id
	})
	for _, row := range rows {
		if err := appendRow(&db, row.kind, row.raw); err != nil {
			return err
		}
	}
	s.db = db
	return nil
}

func writeSQLiteRow(tx *sql.Tx, c Change) error {
	var factRowID int64
	if c.Kind == "fact" && c.Operation == "delete" {
		if err := tx.QueryRow("SELECT rowid FROM objects WHERE kind='fact' AND id=?", c.ID).Scan(&factRowID); err != nil && err != sql.ErrNoRows {
			return err
		}
	}
	if c.Operation == "delete" {
		if _, err := tx.Exec("DELETE FROM objects WHERE kind=? AND id=?", c.Kind, c.ID); err != nil {
			return err
		}
	} else {
		if _, err := tx.Exec(`INSERT INTO objects VALUES(?,?,?,?,?,?) ON CONFLICT(kind,id) DO UPDATE SET position=excluded.position,version=excluded.version,seq=excluded.seq,payload=excluded.payload`, c.Kind, c.ID, c.Position, c.Version, c.Seq, []byte(c.Value)); err != nil {
			return err
		}
	}
	if historyKind(c.Kind) && c.Operation != "move" {
		raw, _ := json.Marshal(c)
		if _, err := tx.Exec("INSERT INTO versions VALUES(?,?,?,?,?)", c.Seq, c.Kind, c.ID, c.Version, raw); err != nil {
			return err
		}
	}
	owner := recordKey(c.Kind, c.ID)
	if _, err := tx.Exec("DELETE FROM relations WHERE owner=?", owner); err != nil {
		return err
	}
	link := func(source, target, kind string) error {
		_, err := tx.Exec("INSERT OR IGNORE INTO relations VALUES(?,?,?,?)", owner, source, target, kind)
		return err
	}
	if c.Kind == "fact" {
		if c.Operation != "delete" {
			if err := tx.QueryRow("SELECT rowid FROM objects WHERE kind='fact' AND id=?", c.ID).Scan(&factRowID); err != nil {
				return err
			}
		}
		if _, err := tx.Exec("DELETE FROM fact_search WHERE rowid=?", factRowID); err != nil {
			return err
		}
		if c.Operation != "delete" {
			var f model.Fact
			if err := json.Unmarshal(c.Value, &f); err != nil {
				return err
			}
			if _, err := tx.Exec("INSERT INTO fact_search(rowid,id,title,summary,doc) VALUES(?,?,?,?,?)", factRowID, f.ID, f.Title, f.Summary, f.Doc); err != nil {
				return err
			}
			if f.IntentID != "" {
				if err := link("intent:"+f.IntentID, owner, "produces"); err != nil {
					return err
				}
			}
			for _, id := range f.EvidenceIDs {
				if err := link(owner, "evidence:"+id, "evidence"); err != nil {
					return err
				}
			}
		}
	}
	if c.Kind == "intent" && c.Operation != "delete" {
		var in model.Intent
		if err := json.Unmarshal(c.Value, &in); err != nil {
			return err
		}
		for _, id := range in.FromFactIDs {
			if err := link("fact:"+id, owner, "premise"); err != nil {
				return err
			}
		}
		for _, id := range in.ContextFactIDs {
			if err := link("fact:"+id, owner, "reference"); err != nil {
				return err
			}
		}
		// Pins are recorded focus, not premises or references, so they get their
		// own relation kind. The same fact may appear in both ContextFactIDs and
		// PinnedFactIDs; the (owner,source,target,relation) primary key keeps the
		// reference and pin rows side by side instead of one replacing the other.
		//
		// relations is a derived index rebuilt per written owner: a ledger
		// indexed before pin support picks these rows up when the intent is next
		// written (or through the explicit `ledger -reindex` maintenance
		// command). The startup path deliberately does not scan history, so do
		// not turn that into a migration here.
		for _, id := range in.PinnedFactIDs {
			if err := link("fact:"+id, owner, "pin"); err != nil {
				return err
			}
		}
	}
	if c.Kind == "goal" && c.Operation != "delete" {
		var g model.Goal
		if err := json.Unmarshal(c.Value, &g); err != nil {
			return err
		}
		for _, id := range g.From {
			if err := link("fact:"+id, owner, "supports"); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Store) Close() error {
	if s.sqlite != nil {
		return s.sqlite.db.Close()
	}
	return nil
}

// FullTextCandidates uses the same trigram index for mixed Chinese/English
// text. Short terms are intentionally handled by the complete lexical path.
func (s *Store) FullTextCandidates(q string) (map[string]float64, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.fullTextCandidates(q)
}
func (s *Store) FullTextCandidatesAt(q string, v ReadVersion) (map[string]float64, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.db.Memory == nil || v.Epoch != s.db.Memory.Epoch || v.Seq != s.db.Memory.Seq {
		return nil, false, nil
	}
	return s.fullTextCandidates(q)
}
func (s *Store) fullTextCandidates(q string) (map[string]float64, bool, error) {
	if s.sqlite == nil {
		return nil, false, nil
	}
	terms := strings.Fields(q)
	quoted := []string{}
	for _, t := range terms {
		if len([]rune(t)) < 3 {
			return nil, false, nil
		}
		quoted = append(quoted, "\""+strings.ReplaceAll(t, "\"", "\"\"")+"\"")
	}
	if len(quoted) == 0 {
		return nil, false, nil
	}
	rows, err := s.sqlite.db.Query("SELECT id,bm25(fact_search,0,8,3,1) FROM fact_search WHERE fact_search MATCH ?", strings.Join(quoted, " AND "))
	if err != nil {
		return nil, true, err
	}
	defer rows.Close()
	out := map[string]float64{}
	for rows.Next() {
		var id string
		var score float64
		if err = rows.Scan(&id, &score); err != nil {
			return nil, true, err
		}
		out[id] = score
	}
	return out, true, rows.Err()
}
