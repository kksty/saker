package api

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/kksty/saker/internal/executor"
	"github.com/kksty/saker/internal/model"
	"github.com/kksty/saker/internal/store"
)

// The advisory occupied by probeSection is deliberately one constant: the
// renderer writes this string and the tests compare against the same string, so
// a copy that drifts (or a test that asserts its own literal) can never make the
// "advisory leaked into a report" check pass by accident.
const (
	probeTableTitle  = "## 这些地址本意图已经探过（只覆盖直接写在命令里的地址，可能不全；命令原文摘录，不是事实，不进报告）"
	probeTableFooter = "这张表只来自命令里直接出现的 IP:端口，用文件清单或变量间接指出的目标看不到；它不构成覆盖证明。要不要再探、探到什么程度，由你判断。"
	// probeTableMax bounds the section. A direction that probed 300 addresses
	// needs the newest 40 and a count, not 300 lines in every task book.
	probeTableMax = 40
	// probeCommandRunes clips the command's first line: it is an excerpt that
	// says "this is what you ran", not a second copy of the journal.
	probeCommandRunes = 120
	// probeLineMax bounds one journal line. Tool-call payloads carry the command
	// text and can be large; a line beyond this is not a probe.
	probeLineMax = 8 << 20
)

// probeAddressPattern is the whole extraction rule: a literal IPv4 address
// followed by a literal port, as written in a command. Addresses reached
// through a file list (-iL, --target-file), a variable, a CIDR or a hostname are
// deliberately not resolved — this table is a reminder, never a coverage proof,
// and the operator-facing sentence says so.
var probeAddressPattern = regexp.MustCompile(`\b(\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}):(\d{2,5})\b`)

// probeEntry is one address this direction has already probed.
type probeEntry struct {
	addr string
	// runs is how many of this direction's runs touched the address, not how
	// many times the command ran: one run that curls an address five times is
	// still one run that has seen it.
	runs    int
	lastRun string
	lastAt  time.Time
	command string
	// others counts distinct other directions in this project that also probed
	// the address. Only the count crosses the direction boundary: the pilot
	// must not carry another direction's narrative into this one's task book.
	others   int
	otherSet map[string]bool
}

// probeObservation is what one journal says about one address.
type probeObservation struct {
	at      time.Time
	command string
}

// probeSection renders what this direction's own run journals say it has
// already touched. Empty when there is nothing to report.
//
// It reads journals only: no table, no field, no ledger write. A journal line is
// already redacted by the writer, so the command excerpt inherits that boundary.
func (s *Server) probeSection(in model.Intent, db store.DB) string {
	if s == nil || strings.TrimSpace(s.DataDir) == "" {
		return ""
	}
	dir := filepath.Join(s.DataDir, "runs")
	own := make(map[string]*probeEntry)
	ownRuns := make(map[string]bool)
	for _, run := range db.Runs {
		if run.IntentID != in.ID || run.ID == in.RunID {
			continue
		}
		ownRuns[run.ID] = true
		obs, ok := readProbeJournal(filepath.Join(dir, run.ID+".jsonl"))
		if !ok {
			continue
		}
		for addr, o := range obs {
			e, exists := own[addr]
			if !exists {
				e = &probeEntry{addr: addr}
				own[addr] = e
			}
			e.runs++
			if e.lastAt.IsZero() || o.at.After(e.lastAt) {
				e.lastAt, e.lastRun, e.command = o.at, run.ID, o.command
			}
		}
	}
	if len(own) == 0 {
		return ""
	}
	// Only now is it worth reading other directions: with no entries of our own
	// there is nothing for them to attach to.
	for _, run := range db.Runs {
		if run.IntentID == "" || run.IntentID == in.ID || ownRuns[run.ID] {
			continue
		}
		obs, ok := readProbeJournal(filepath.Join(dir, run.ID+".jsonl"))
		if !ok {
			continue
		}
		for addr := range obs {
			if e, ok := own[addr]; ok {
				if e.otherSet == nil {
					e.otherSet = make(map[string]bool)
				}
				e.otherSet[run.IntentID] = true
			}
		}
	}
	entries := make([]*probeEntry, 0, len(own))
	for _, e := range own {
		e.others = len(e.otherSet)
		e.otherSet = nil
		entries = append(entries, e)
	}
	sort.Slice(entries, func(i, j int) bool {
		if !entries[i].lastAt.Equal(entries[j].lastAt) {
			return entries[i].lastAt.After(entries[j].lastAt)
		}
		return entries[i].addr < entries[j].addr
	})
	hidden := 0
	if len(entries) > probeTableMax {
		hidden, entries = len(entries)-probeTableMax, entries[:probeTableMax]
	}

	var b strings.Builder
	b.WriteString("\n")
	b.WriteString(probeTableTitle)
	b.WriteString("\n")
	for _, e := range entries {
		fmt.Fprintf(&b, "- %s — 探过 %d 次，最近 run %s（%s）\n",
			e.addr, e.runs, e.lastRun, e.lastAt.Format("2006-01-02 15:04"))
		if e.command != "" {
			fmt.Fprintf(&b, "  原命令首行：%s\n", e.command)
		}
		if e.others > 0 {
			fmt.Fprintf(&b, "  另有 %d 个意图也探过\n", e.others)
		}
	}
	if hidden > 0 {
		fmt.Fprintf(&b, "- 还有 %d 个更早探过的地址未列出。\n", hidden)
	}
	b.WriteString(probeTableFooter)
	b.WriteString("\n")
	return b.String()
}

// readProbeJournal extracts, for one run, every literal host:port its bash tool
// calls named, with the newest command that named it. A missing or unreadable
// journal is "nothing to say", not an error: the task book must not fail because
// a run from last week left no log.
func readProbeJournal(path string) (map[string]probeObservation, bool) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false
	}
	defer f.Close()
	out := make(map[string]probeObservation)
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), probeLineMax)
	first := true
	for sc.Scan() {
		line := sc.Bytes()
		if first {
			first = false
			// A journal from another build is not read here either: the task
			// book says nothing about it instead of quoting half-understood
			// commands.
			if journalFileVersion(line) != executor.JournalSchemaVersion {
				return nil, false
			}
		}
		if !bytes.Contains(line, []byte(`"tool_call"`)) {
			continue
		}
		var ev struct {
			Kind string          `json:"kind"`
			Tool string          `json:"tool"`
			At   time.Time       `json:"at"`
			Raw  json.RawMessage `json:"raw"`
		}
		if json.Unmarshal(line, &ev) != nil || ev.Kind != "tool_call" || ev.Tool != "bash" {
			continue
		}
		command := toolCommand(ev.Raw)
		if command == "" {
			continue
		}
		excerpt := firstLineExcerpt(command, probeCommandRunes)
		for _, m := range probeAddressPattern.FindAllStringSubmatch(command, -1) {
			if ignoredProbeHost(m[1]) {
				continue
			}
			addr := m[1] + ":" + m[2]
			if prev, ok := out[addr]; ok && !ev.At.After(prev.at) {
				continue
			}
			out[addr] = probeObservation{at: ev.At, command: excerpt}
		}
	}
	return out, true
}

// ignoredProbeHost drops loopback and the wildcard: a run that curls its own
// control plane has not probed a target.
func ignoredProbeHost(host string) bool {
	return host == "0.0.0.0" || strings.HasPrefix(host, "127.")
}

// firstLineExcerpt returns the first non-empty line of a command, clipped to n
// runes. The first line is what identifies the probe; the rest is the pipeline.
func firstLineExcerpt(command string, n int) string {
	for _, line := range strings.Split(command, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		r := []rune(line)
		if len(r) > n {
			return string(r[:n]) + "…"
		}
		return line
	}
	return ""
}
