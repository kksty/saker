package api

import (
	"bufio"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/kksty/saker/internal/executor"
	"github.com/kksty/saker/internal/platform"
)

// ToolStat is one row of the tool-call leaderboard. Calls are counted from the
// run logs (kind=tool_call), not from facts: a call that produced nothing still
// cost a turn, and that is exactly what the ranking is for.
type ToolStat struct {
	Tool       string     `json:"tool"`
	Calls      int        `json:"calls"`
	Runs       int        `json:"runs"`
	LastAt     *time.Time `json:"last_at,omitempty"`
	Local      bool       `json:"local,omitempty"`
	Completed  int        `json:"completed"`
	Failures   int        `json:"failures"`
	DurationMS int64      `json:"duration_ms,omitempty"`
	// ErrorKinds is the top three normalised failure labels for this tool. The
	// journal does not carry a structured error code, so the label is read from
	// the failed result's own text and bucketed ("exit code 1", "unsupported
	// flag --tlsv1.2", "executable file not found"). It is a reading aid, not a
	// taxonomy: at most three buckets, each counted.
	ErrorKinds []ToolErrorKind `json:"error_kinds,omitempty"`
}

// ToolErrorKind is one normalised failure label with how often it happened.
type ToolErrorKind struct {
	Label string `json:"label"`
	Count int    `json:"count"`
}

// ArsenalTool is one binary found in an arsenal bin directory, with the
// version/managed_by fields from that directory's manifest.json when present.
// Dirs lists every arsenal root that holds the same binary: an operator may
// have installed it both instance-wide and for the local user, and the page
// should say so once, not twice.
type ArsenalTool struct {
	Name        string   `json:"name"`
	Version     string   `json:"version,omitempty"`
	Size        int64    `json:"size,omitempty"`
	Source      string   `json:"source,omitempty"`
	ManagedBy   string   `json:"managed_by,omitempty"`
	InstalledAt string   `json:"installed_at,omitempty"`
	Dirs        []string `json:"dirs,omitempty"`
}

// toolRoot is one arsenal root the local harness scan node may resolve tool
// binaries from. The root holds bin/ (the executables) and manifest.json (what
// arsenal installed); the executable directory is what the runtime prepends to
// the model's PATH (harness cmd/cyber-scan/extensions.go:37-41).
//
// A root is a location, not a claim that a tool is usable: "文件存在不等于可用"
// (AGENTS.md). Whether the host can actually run commands is reported by the
// runtime catalogue in the same response, never inferred from this list.
type toolRoot struct {
	Path   string `json:"path"`
	Bin    string `json:"bin"`
	Source string `json:"source"` // instance | harness | user | configured
	Exists bool   `json:"exists"` // <path>/bin exists
	// Resolved marks the root the running scan resolves by the harness's own
	// data-directory precedence. Only one root is marked when it can be derived.
	Resolved bool `json:"resolved,omitempty"`
	// PathInjected marks the directory the runtime prepends to the child PATH.
	PathInjected bool   `json:"path_injected,omitempty"`
	Note         string `json:"note,omitempty"`
}

type toolsResponse struct {
	Leaderboard []ToolStat    `json:"leaderboard"`
	RunsScanned int           `json:"runs_scanned"`
	Arsenal     []ArsenalTool `json:"arsenal"`
	ArsenalDirs []string      `json:"arsenal_dirs,omitempty"`
	// ToolRoots lists every candidate root with its source, so the console can
	// show where the harness will actually find tools instead of one guessed
	// directory. The historical arsenal/arsenal_dirs fields are kept verbatim.
	ToolRoots []toolRoot `json:"tool_roots,omitempty"`
	// ToolRootsNote explains how the roots were derived and what cannot be
	// observed from the runtime, so a missing root is explainable.
	ToolRootsNote string `json:"tool_roots_note,omitempty"`
	// Runtime is the live host catalogue (commands/model/health). A file that
	// exists but is not declared by the running harness is not promoted to
	// "available": the state and error here are the availability answer.
	Runtime *runtimeToolsResponse `json:"runtime,omitempty"`
}

// toolFile is one run log's contribution, cached until the file changes.
type toolFile struct {
	size      int64
	mod       time.Time
	calls     map[string]int
	last      map[string]time.Time
	completed map[string]int
	failures  map[string]int
	duration  map[string]int64
	// errors: tool → normalised failure label → count. Read from the failed
	// result's own text; see failureLabel.
	errors map[string]map[string]int
}

func (s *Server) listTools(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "只支持 GET"})
		return
	}
	roots := s.toolRoots()
	arsenal, dirs := s.arsenalTools(roots)
	rows, scanned := s.toolLeaderboard(arsenal)
	// Availability is the running host's declaration, not a filesystem guess: a
	// dead host reports state/error instead of an empty "successful" catalogue.
	runtime := s.runtimeCatalogue(r.Context(), runtimeCatalogueTimeout)
	writeJSON(w, http.StatusOK, toolsResponse{
		Leaderboard:   rows,
		RunsScanned:   scanned,
		Arsenal:       arsenal,
		ArsenalDirs:   dirs,
		ToolRoots:     roots,
		ToolRootsNote: toolRootsNote,
		Runtime:       &runtime,
	})
}

// toolLeaderboard aggregates tool_call events over every run log of the
// project. A run log is re-read only when its size or mtime changed, so the
// page stays cheap after the first load even with large JSONL files.
//
// Callers that already listed the arsenal pass it in: the same request used to
// scan every tool root twice (once here, once for the response) on a page the
// console polls every ten seconds.
func (s *Server) toolLeaderboard(scannedArsenal ...[]ArsenalTool) ([]ToolStat, int) {
	dir := filepath.Join(s.DataDir, "runs")
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, 0
	}

	type tally struct {
		calls               int
		runs                int
		last                time.Time
		completed, failures int
		duration            int64
		errors              map[string]int
	}
	var arsenal []ArsenalTool
	if len(scannedArsenal) > 0 {
		arsenal = scannedArsenal[0]
	} else {
		arsenal, _ = s.arsenalTools(s.toolRoots())
	}
	localNames := map[string]bool{}
	for _, name := range strings.Fields("gogo curl katana spray playwright proton neutron passive zombie scan loop mitm fetch rg python python3 node") {
		localNames[name] = true
	}
	for _, tool := range arsenal {
		localNames[strings.ToLower(tool.Name)] = true
	}
	byTool := map[string]*tally{}
	scanned := 0

	for _, ent := range ents {
		if ent.IsDir() || !strings.HasSuffix(ent.Name(), ".jsonl") {
			continue
		}
		path := filepath.Join(dir, ent.Name())
		st, err := os.Stat(path)
		if err != nil {
			continue
		}
		scanned++

		s.mu.Lock()
		cached, ok := s.toolCache[path]
		s.mu.Unlock()
		if !ok || cached.size != st.Size() || !cached.mod.Equal(st.ModTime()) {
			cached = readToolCalls(path, st)
			s.mu.Lock()
			s.toolCache[path] = cached
			s.mu.Unlock()
		}

		for tool, n := range cached.calls {
			if strings.HasPrefix(tool, "local:") && !localNames[strings.TrimPrefix(tool, "local:")] {
				continue
			}
			t := byTool[tool]
			if t == nil {
				t = &tally{}
				byTool[tool] = t
			}
			t.calls += n
			t.completed += cached.completed[tool]
			t.failures += cached.failures[tool]
			t.duration += cached.duration[tool]
			for label, count := range cached.errors[tool] {
				if t.errors == nil {
					t.errors = map[string]int{}
				}
				t.errors[label] += count
			}
			t.runs++
			if at := cached.last[tool]; at.After(t.last) {
				t.last = at
			}
		}
	}

	out := make([]ToolStat, 0, len(byTool))
	for tool, t := range byTool {
		row := ToolStat{Tool: tool, Calls: t.calls, Runs: t.runs}
		row.Completed, row.Failures, row.DurationMS = t.completed, t.failures, t.duration
		row.ErrorKinds = topErrorKinds(t.errors, 3)
		if strings.HasPrefix(tool, "local:") {
			row.Tool = strings.TrimPrefix(tool, "local:")
			row.Local = true
		}
		if !t.last.IsZero() {
			at := t.last
			row.LastAt = &at
		}
		out = append(out, row)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Calls != out[j].Calls {
			return out[i].Calls > out[j].Calls
		}
		return out[i].Tool < out[j].Tool
	})
	return out, scanned
}

// readToolCalls reads one run log and keeps only what the leaderboard needs.
func readToolCalls(path string, st os.FileInfo) toolFile {
	out := toolFile{
		size:      st.Size(),
		mod:       st.ModTime(),
		calls:     map[string]int{},
		last:      map[string]time.Time{},
		completed: map[string]int{}, failures: map[string]int{}, duration: map[string]int64{},
		errors: map[string]map[string]int{},
	}
	f, err := os.Open(path)
	if err != nil {
		return out
	}
	defer f.Close()

	reader := bufio.NewReaderSize(f, 1<<20)
	toolsByCall := map[string]string{}
	first := true
	for {
		line, readErr := reader.ReadString('\n')
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			if first {
				first = false
				// Another build wrote this journal: the leaderboard says nothing
				// about it rather than reading fields this build may misread.
				if journalFileVersion([]byte(trimmed)) != executor.JournalSchemaVersion {
					return out
				}
			}
			var ev struct {
				Kind       string          `json:"kind"`
				Tool       string          `json:"tool"`
				At         time.Time       `json:"at"`
				CallID     string          `json:"call_id"`
				OK         *bool           `json:"ok"`
				Text       string          `json:"text"`
				DurationMS int64           `json:"duration_ms"`
				Raw        json.RawMessage `json:"raw"`
			}
			if json.Unmarshal([]byte(trimmed), &ev) == nil && ev.Kind == "tool_call" && ev.Tool != "" {
				out.calls[ev.Tool]++
				if ev.CallID != "" {
					toolsByCall[ev.CallID] = ev.Tool
				}
				if ev.Tool == "bash" {
					commands := shellCommands(toolCommand(ev.Raw))
					for _, command := range commands {
						key := "local:" + command
						out.calls[key]++
						if ev.At.After(out.last[key]) {
							out.last[key] = ev.At
						}
					}
				}
				if ev.At.After(out.last[ev.Tool]) {
					out.last[ev.Tool] = ev.At
				}
			}
			if ev.Kind == "tool_result" {
				tool := ev.Tool
				if tool == "" {
					tool = toolsByCall[ev.CallID]
				}
				delete(toolsByCall, ev.CallID)
				if tool != "" {
					out.completed[tool]++
					// Failure is only counted when the result says so. A result
					// that omits the field is unknown, not a failure: the harness
					// sets it for every tool result it emits, so silence means the
					// writer had nothing to report.
					if failed(ev.OK) {
						out.failures[tool]++
						if label := failureLabel(ev.Text, ev.Raw); label != "" {
							bucket := out.errors[tool]
							if bucket == nil {
								bucket = map[string]int{}
								out.errors[tool] = bucket
							}
							bucket[label]++
						}
					}
					out.duration[tool] += ev.DurationMS
				}
			}
		}
		if readErr != nil {
			break
		}
	}
	return out
}

// toolRoots is the candidate set the console reports. An explicit override
// (tests, alternate deployments) replaces the derived roots; otherwise the
// roots come from the harness's own data-directory rule, so the page names the
// directory the running scan actually reads instead of a guessed one.
func (s *Server) toolRoots() []toolRoot {
	if len(s.ArsenalDirs) > 0 {
		out := make([]toolRoot, 0, len(s.ArsenalDirs))
		for _, root := range s.ArsenalDirs {
			out = append(out, newToolRoot(root, "configured"))
		}
		return out
	}
	return defaultToolRoots(s.DataDir)
}

func newToolRoot(root, source string) toolRoot {
	root = filepath.Clean(root)
	out := toolRoot{Path: root, Bin: filepath.Join(root, "bin"), Source: source}
	if st, err := os.Stat(out.Bin); err == nil && st.IsDir() {
		out.Exists = true
	}
	return out
}

// toolRootsNote is operator-facing provenance, not tool usage: it says which
// resolution rule produced the roots and what the console cannot infer.
const toolRootsNote = "按 harness 数据目录规则推导：scan 工作目录下的 .cyber 优先，其次 ~/.cyber；配置里显式的 misc.data_dir 与 scan 可执行文件旁的 .cyber 读不到，以宿主实际配置为准。"

// instanceDataDir turns the project directory into the instance root. In
// production DataDir is <data>/projects/<id>; a bare server (tests, alternate
// launchers) passes its own root, and then that root is the instance.
func instanceDataDir(projectDir string) string {
	clean := filepath.Clean(projectDir)
	if filepath.Base(filepath.Dir(clean)) == "projects" {
		return filepath.Dir(filepath.Dir(clean))
	}
	return clean
}

// defaultToolRoots derives the roots from the harness's data-directory rule
// (cyber-harness pkg/config/datadir.go:9-26):
//
//	scan workdir/.cyber, then <scan executable dir>/.cyber, then ~/.cyber
//
// saker launches the scan with its working directory set to the instance data
// directory (internal/host/host.go execLaunch), so the first candidate is
// <instance>/.cyber and the last is the user directory. The scan executable
// directory is not observable from the AOP runtime, so it is not guessed; the
// note says so. The legacy instance root (<instance>/arsenal) appears only when
// tools are installed there, since that is the only case where it is real.
func defaultToolRoots(projectDir string) []toolRoot {
	instance := instanceDataDir(projectDir)
	var roots []toolRoot
	// The legacy instance root (<instance>/arsenal) is only listed when tools
	// actually sit there. A candidate that cannot exist is not information: it
	// reads as a defect next to the root the scan really resolves.
	if legacy := newToolRoot(filepath.Join(instance, "arsenal"), "instance"); legacy.Exists {
		roots = append(roots, legacy)
	}

	harnessData := filepath.Join(instance, ".cyber")
	harness := newToolRoot(filepath.Join(harnessData, "arsenal"), "harness")
	if st, err := os.Stat(harnessData); err == nil && st.IsDir() {
		harness.Resolved = true
		harness.PathInjected = true
		// Resolved says where the scan looks; Exists says whether anything is
		// installed there. Both are true at once, and an operator reading "解析到
		// 此" next to "目录不存在" takes it as a defect, so say it as one state.
		if !harness.Exists {
			harness.Note = "harness 解析到此，还没有装任何工具"
		}
	} else {
		harness.Note = "scan 工作目录下还没有 .cyber；harness 回退到用户目录"
	}
	roots = append(roots, harness)

	if home, err := os.UserHomeDir(); err == nil {
		user := newToolRoot(filepath.Join(home, ".cyber", "arsenal"), "user")
		// The user root is the harness fallback only while <instance>/.cyber is
		// absent; once it exists the scan never reads the user arsenal.
		user.Resolved = !harness.Resolved
		user.PathInjected = user.Resolved
		if harness.Resolved {
			user.Note = "harness 已改用 scan 工作目录下的 .cyber，这里不进 PATH"
		}
		roots = append(roots, user)
	}
	return roots
}

// arsenalTools lists what arsenal installed on this machine, per candidate root.
// The list is an inventory: the same response carries the runtime catalogue, and
// a file that exists but is not declared by the running host is not called
// "available" here.
func (s *Server) arsenalTools(roots []toolRoot) ([]ArsenalTool, []string) {
	seen := map[string]bool{}
	byName := map[string][]ArsenalTool{}
	var dirs []string
	for _, root := range roots {
		bin := root.Bin
		st, err := os.Stat(bin)
		if err != nil || !st.IsDir() || seen[bin] {
			continue
		}
		seen[bin] = true
		dirs = append(dirs, bin)

		manifest := readArsenalManifest(filepath.Join(root.Path, "manifest.json"))
		ents, err := os.ReadDir(bin)
		if err != nil {
			continue
		}
		for _, ent := range ents {
			name := ent.Name()
			if ent.IsDir() || strings.HasPrefix(name, ".") {
				continue
			}
			name = platform.TrimExecutableSuffix(name)
			info, err := ent.Info()
			if err != nil {
				continue
			}
			row := ArsenalTool{Name: name, Size: info.Size(), Dirs: []string{bin}}
			if m, ok := manifest[name]; ok {
				row.Version = m.Version
				row.Source = m.Source
				row.ManagedBy = m.ManagedBy
				row.InstalledAt = m.InstalledAt
			}
			byName[name] = append(byName[name], row)
		}
	}

	out := make([]ArsenalTool, 0, len(byName))
	for _, rows := range byName {
		pick := rows[0]
		var where []string
		for _, row := range rows {
			where = append(where, row.Dirs[0])
			switch {
			case pick.Version == "" && row.Version != "":
				pick = row // prefer the copy the manifest describes
			case row.Version == pick.Version && row.Size > pick.Size:
				pick = row // a stale 1-byte placeholder must not be the headline
			}
		}
		// Show the directory of the copy the page is describing first.
		for i, dir := range where {
			if dir == pick.Dirs[0] {
				where[0], where[i] = where[i], where[0]
				break
			}
		}
		pick.Dirs = where
		out = append(out, pick)
	}

	sort.Slice(out, func(i, j int) bool {
		return out[i].Name < out[j].Name
	})
	return out, dirs
}

type arsenalManifestEntry struct {
	Version     string `json:"version"`
	InstalledAt string `json:"installed_at"`
	Source      string `json:"source"`
	ManagedBy   string `json:"managed_by"`
}

func readArsenalManifest(path string) map[string]arsenalManifestEntry {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var m map[string]arsenalManifestEntry
	if json.Unmarshal(data, &m) != nil {
		return nil
	}
	return m
}

// topErrorKinds returns the n most frequent labels, ties broken by label so a
// re-read of the same logs yields the same order.
func topErrorKinds(counts map[string]int, n int) []ToolErrorKind {
	if len(counts) == 0 {
		return nil
	}
	out := make([]ToolErrorKind, 0, len(counts))
	for label, count := range counts {
		out = append(out, ToolErrorKind{Label: label, Count: count})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Label < out[j].Label
	})
	if len(out) > n {
		out = out[:n]
	}
	return out
}

// failed reports whether a tool_result is a failure. Only an explicit false is
// a failure; a missing bit says nothing and is never counted.
func failed(ok *bool) bool {
	return ok != nil && !*ok
}

// failureLabel reads one failed tool result and returns a short, stable bucket.
//
// The journal carries no error code, so the label comes from the result's own
// text: a shell exit status, a rejected flag, or why a binary could not start.
// Known shapes map to fixed labels or a parsed numeric exit code. Unknown
// text is counted under one label; its original lives in the journal.
//
// The text must be read from the message body only. Walking the whole event for
// string leaves picked up the envelope (id, emitted_at, turn_id, seq) in Go's
// random map order, so the same result produced a different label on every
// scan; the envelope is never a candidate.
func failureLabel(text string, raw json.RawMessage) string {
	candidates := make([]string, 0, 4)
	if strings.TrimSpace(text) != "" {
		candidates = append(candidates, text)
	}
	candidates = append(candidates, resultTexts(raw)...)
	for _, candidate := range candidates {
		for _, line := range strings.Split(candidate, "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			if label := normalizeFailureLine(line); label != "" {
				return label
			}
		}
	}
	return "other failure"
}

// resultTexts returns the output texts a tool result carries, in the order the
// result wrote them. It only looks at the documented result shape: the
// tool_result.output array, each item's text (either a string or {text: …}),
// and a plain string raw. Any other shape yields nothing rather than guessing.
func resultTexts(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var plain string
	if json.Unmarshal(raw, &plain) == nil {
		return []string{plain}
	}
	var envelope struct {
		ToolResult struct {
			Output []json.RawMessage `json:"output"`
		} `json:"tool_result"`
	}
	if json.Unmarshal(raw, &envelope) != nil {
		return nil
	}
	out := make([]string, 0, len(envelope.ToolResult.Output))
	for _, item := range envelope.ToolResult.Output {
		if text := outputText(item); text != "" {
			out = append(out, text)
		}
	}
	return out
}

// outputText reads one output item, which is either a string or an object whose
// text is a string or another {text: …} object.
func outputText(item json.RawMessage) string {
	var str string
	if json.Unmarshal(item, &str) == nil {
		return str
	}
	var probe struct {
		Text json.RawMessage `json:"text"`
	}
	if json.Unmarshal(item, &probe) != nil || len(probe.Text) == 0 {
		return ""
	}
	if json.Unmarshal(probe.Text, &str) == nil {
		return str
	}
	var nested struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(probe.Text, &nested) == nil {
		return nested.Text
	}
	return ""
}

// normalizeFailureLine maps one line to a known failure bucket, or "" when the
// line says nothing recognisable.
func normalizeFailureLine(line string) string {
	for _, marker := range []string{"[exit code: ", "exit code: ", "exit status ", "exit code "} {
		if i := strings.Index(line, marker); i >= 0 {
			if code, err := strconv.ParseInt(leadingToken(line[i+len(marker):]), 10, 64); err == nil {
				return "exit code " + strconv.FormatInt(code, 10)
			}
		}
	}
	if strings.Contains(line, "unsupported flag ") {
		return "unsupported flag"
	}
	for _, known := range []struct{ needle, label string }{
		{"executable file not found", "executable file not found"},
		{"command not found", "command not found"},
		{"No such file or directory", "no such file or directory"},
		{"no such file or directory", "no such file or directory"},
		{"permission denied", "permission denied"},
		{"Permission denied", "permission denied"},
		{"Traceback (most recent call last)", "Traceback"},
		{"context deadline exceeded", "timeout"},
		{"timed out", "timeout"},
	} {
		if strings.Contains(line, known.needle) {
			return known.label
		}
	}
	return ""
}

// leadingToken reads the first token after a marker, stopping at the delimiters
// the shell and curl wrappers actually print (`--tlsv1.2]`, `1:`, `2)`).
func leadingToken(s string) string {
	s = strings.TrimLeft(s, " \t:=[(")
	var b strings.Builder
	for _, r := range s {
		if r == ' ' || r == '\t' || r == ']' || r == ')' || r == ',' || r == ':' {
			break
		}
		b.WriteRune(r)
	}
	return b.String()
}

// clipRunes truncates on a rune boundary, never inside a UTF-8 sequence.
