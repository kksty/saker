package main

import (
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/kksty/saker/internal/vulnlib"
)

// vulnQuarantine is one vulnerability-library index the instance moved aside at
// startup. It rides along with the project index (GET /api/projects) for the
// same reason skipped_projects does: a degradation the operator must know about
// cannot live only in the process log. A quarantined index means the library
// started empty, so a search can be missing entries until someone reads the
// file and repairs or re-imports it by hand.
type vulnQuarantine struct {
	// Path is where the unparseable index was moved; the bytes are intact.
	Path string `json:"path"`
	// Reason is the parse error, kept verbatim: it is what tells a truncated
	// write from a mangled one.
	Reason string `json:"reason"`
	At     time.Time `json:"quarantined_at"`
}

// openVulnLibrary opens the instance-level vulnerability library.
//
// A corrupt index must not stop the instance. It is a secondary index: the
// engagement's own ledger is the record, this file mirrors it, and a project
// that fails to load already degrades only itself (main.go 的项目加载降级,
// hub.go 的 skipped_projects). Dying here would mean one truncated JSON file
// takes every project down with it, which is the opposite order of importance.
//
// So the bytes are renamed aside — kept, and named so the operator can find them
// — and startup continues with an empty library. The quarantine is returned, not
// just logged, so the console can say it. On success the caller always gets a
// usable library: the hub calls methods on it without a nil check, so nil is
// never returned with a nil error.
//
// Any other open error is returned unchanged: an index that cannot be read (not
// a parse failure) cannot be written safely either, and continuing would drop
// later declarations without saying so.
func openVulnLibrary(dir string) (*vulnlib.Library, *vulnQuarantine, error) {
	lib, err := vulnlib.Open(dir)
	if err == nil {
		return lib, nil, nil
	}
	if !errors.Is(err, vulnlib.ErrCorrupt) {
		return nil, nil, err
	}
	moved, qerr := vulnlib.QuarantineCorruptIndex(dir)
	if qerr != nil {
		return nil, nil, fmt.Errorf("索引损坏（%v），另存失败，未覆盖原文件：%w", err, qerr)
	}
	log.Printf("漏洞库索引损坏，已另存为 %s，以空库继续启动：%v", moved, err)
	lib, oerr := vulnlib.Open(dir)
	if oerr != nil {
		return nil, nil, oerr
	}
	return lib, &vulnQuarantine{Path: moved, Reason: err.Error(), At: time.Now()}, nil
}
