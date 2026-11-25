// Read-only ledger access for tw-metrics.
//
// store.ReadSnapshot opens SQLite with mode=ro, but a WAL-mode database still
// needs its -shm/-wal siblings; on a writable directory SQLite may create them,
// which would write next to the project data. tw-metrics therefore never uses a
// plain mode=ro open on the source. It reads the committed rows through an
// explicit immutable=1 URI (SQLite guarantees no locking files and no writes),
// and only when that is impossible (a non-empty WAL from a live writer) copies
// the ledger plus its WAL/SHM into a temporary directory for store.ReadSnapshot.
package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"

	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/store"
)

// immutableMeta is the committed meta row. It is decoded into the same
// store.Memory type the writer uses, so the metrics see exactly the ledger's
// control state without a second interpretation.
type immutableMeta struct {
	Version    int           `json:"version"`
	MessageSeq uint64        `json:"message_seq"`
	Memory     *store.Memory `json:"memory"`
}

// readLedgerImmutable reads one committed ledger through SQLite's immutable URI
// mode: no journal, no WAL, no shared-memory file, no lock. It reproduces the
// rows store.ReadSnapshot loads (meta + objects + version history) but only for
// the object kinds the metrics consume; an unknown kind is an error, matching
// the store's own strictness, so a future schema cannot be silently half-read.
func readLedgerImmutable(path string) (store.DB, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return store.DB{}, err
	}
	db, err := sql.Open("sqlite", immutableDSN(abs))
	if err != nil {
		return store.DB{}, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	var raw []byte
	if err := db.QueryRow("SELECT payload FROM meta WHERE id=1").Scan(&raw); err != nil {
		return store.DB{}, fmt.Errorf("读取账本 meta：%w", err)
	}
	var meta immutableMeta
	if err := json.Unmarshal(raw, &meta); err != nil {
		return store.DB{}, fmt.Errorf("解析账本 meta：%w", err)
	}
	if meta.Version != store.CurrentSchemaVersion() {
		return store.DB{}, fmt.Errorf("不支持的账本 schema %d（当前 %d；不做迁移，请用匹配版本的工具读取）", meta.Version, store.CurrentSchemaVersion())
	}
	if meta.Memory == nil {
		return store.DB{}, fmt.Errorf("账本 meta 缺少 memory 控制状态")
	}
	out := store.DB{Version: meta.Version, MessageSeq: meta.MessageSeq, Memory: meta.Memory}

	rows, err := db.Query("SELECT kind,payload FROM objects ORDER BY kind,position,id")
	if err != nil {
		return store.DB{}, fmt.Errorf("读取账本对象：%w", err)
	}
	for rows.Next() {
		var kind string
		var payload []byte
		if err := rows.Scan(&kind, &payload); err != nil {
			_ = rows.Close()
			return store.DB{}, err
		}
		if err := appendImmutableRow(&out, kind, payload); err != nil {
			_ = rows.Close()
			return store.DB{}, err
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return store.DB{}, err
	}
	_ = rows.Close()

	// The version journal carries the notebook/other history the handoff metric
	// recomputes receipts from; it is read after the live rows so the history
	// slice contains exactly the committed rows, as store.ReadSnapshot does.
	rows, err = db.Query("SELECT payload FROM versions ORDER BY seq")
	if err != nil {
		return store.DB{}, fmt.Errorf("读取账本版本历史：%w", err)
	}
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			_ = rows.Close()
			return store.DB{}, err
		}
		var c store.Change
		if err := json.Unmarshal(payload, &c); err != nil {
			_ = rows.Close()
			return store.DB{}, fmt.Errorf("解析账本版本历史：%w", err)
		}
		out.Memory.History = append(out.Memory.History, c)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return store.DB{}, err
	}
	_ = rows.Close()
	return out, nil
}

// appendImmutableRow mirrors internal/store/rows.go for the kinds the metrics
// read. Kinds the metrics do not consume are still decoded into their model type
// (and rejected if unknown), so a row that this reader cannot understand is a
// visible failure rather than silently missing data.
func appendImmutableRow(db *store.DB, kind string, raw []byte) error {
	switch kind {
	case "notebook":
		var v model.Notebook
		if err := json.Unmarshal(raw, &v); err != nil {
			return err
		}
		db.Notebooks = append(db.Notebooks, v)
	case "instruction":
		var v model.Instruction
		if err := json.Unmarshal(raw, &v); err != nil {
			return err
		}
		db.Instructions = append(db.Instructions, v)
	case "fact":
		var v model.Fact
		if err := json.Unmarshal(raw, &v); err != nil {
			return err
		}
		db.Facts = append(db.Facts, v)
	case "intent":
		var v model.Intent
		if err := json.Unmarshal(raw, &v); err != nil {
			return err
		}
		db.Intents = append(db.Intents, v)
	case "run":
		var v model.Run
		if err := json.Unmarshal(raw, &v); err != nil {
			return err
		}
		db.Runs = append(db.Runs, v)
	case "message":
		var v model.Message
		if err := json.Unmarshal(raw, &v); err != nil {
			return err
		}
		db.Messages = append(db.Messages, v)
	case "evidence", "goal", "review", "playbook", "operator_request", "route", "action", "proposal":
		// Not used by the vertical metrics; the row is still part of the ledger
		// contract, so its presence is accepted while its payload is left to the
		// store-backed reader. A corrupt payload of these kinds is outside this
		// tool's measurement surface.
		return nil
	default:
		return fmt.Errorf("未知的账本对象类型 %q：本工具不支持该 schema", kind)
	}
	return nil
}

// immutableDSN builds file:...?mode=ro&immutable=1. immutable=1 tells SQLite the
// file cannot change while open; it therefore skips locking and must not create
// the WAL/SHM side files, which is what makes a read of the source truly
// read-only.
func immutableDSN(path string) string {
	p := filepath.ToSlash(path)
	if filepath.VolumeName(path) != "" {
		p = "/" + p
	}
	u := url.URL{Scheme: "file", Path: p}
	q := url.Values{}
	q.Set("mode", "ro")
	q.Set("immutable", "1")
	q.Add("_pragma", "busy_timeout(5000)")
	u.RawQuery = q.Encode()
	return u.String()
}

// ledgerNeedsCopy reports whether ledger.sqlite-wal carries committed data. A
// non-empty WAL means immutable=1 could miss it; the caller then reads a temp
// copy so the measurement matches what the live store would load.
func ledgerNeedsCopy(ledgerPath string) bool {
	info, err := os.Stat(ledgerPath + "-wal")
	return err == nil && info.Size() > 0
}
