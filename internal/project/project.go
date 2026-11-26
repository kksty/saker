// Package project makes the engagement the unit of isolation.
//
// One process serves several engagements. Each one is a directory under
// <data>/projects/<id> and owns its ledger, decider memory, workspaces, imports
// and bundles. Two projects share no state: a fact declared in one is invisible
// to the other because each decider reads only its own store (docs/02, D33).
//
// The instance-level directories stay shared on purpose: arsenal (tools),
// pocs (templates) and skills (house style) are tooling, not findings. A tool
// installed once is available everywhere; a credential found once is not.
package project

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/kksty/saker/internal/store"
)

// ManifestFile is the identity of a project on disk. The directory name is the
// id used in URLs; the manifest carries the display name and the authorised
// scope, which belong to the engagement rather than to a flag.
const ManifestFile = "project.json"

// PauseKind values are persisted next to the paused flag so a restart can still
// say who stopped the project. They are machine codes: the console and the
// resume contract switch on them, never on the human-readable reason prose.
// api.pauseKind* and cmd/saker's boot reason derive from these constants, so the
// runtime and the manifest cannot drift apart.
const (
	PauseKindManualPause       = "manual_pause"
	PauseKindCompletionStandby = "completion_standby"
	PauseKindCommanderStandby  = "commander_standby"
	// PauseKindRestart is written by the boot path when a control-plane restart
	// forces a project that was running to come up paused. It is its own code
	// because "the operator paused it" is a different fact: without it a restart
	// was reported as a manual pause, and the console told the operator they had
	// stopped something they never touched. It is a crash marker, not a decision:
	// once the boot crash sweep has converged the orphan rows, the watchdog
	// recovery clears it and continues the project on its own.
	PauseKindRestart = "restart_pause"
)

type Manifest struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Scope is the operator's free-text current boundary for this engagement.
	// It is injected into briefs as a snapshot of the current wording and is
	// never parsed, matched against targets, or enforced: assets are added and
	// removed during a test, and the operator updates the boundary through
	// messages/the dispatch window instead of editing a whitelist (D59).
	Scope string `json:"scope,omitempty"`
	// Description is the operator's prose for this engagement: authorised
	// boundaries, forbidden actions, platform rules, how to treat target output.
	// The program injects it into every brief and never parses it (D59).
	Description string    `json:"description,omitempty"`
	Created     time.Time `json:"created"`
	// ScopeUpdatedAt is when the operator last rewrote Scope or Description. It
	// exists only to order the brief against the instruction chain: the newer of
	// the two wins. A zero value means "never revised" (manifests written before
	// this field existed) and must be resolved against Created by the reader —
	// treating zero as the beginning of time would let every instruction outrank
	// a brief that was edited today.
	ScopeUpdatedAt time.Time `json:"scope_updated_at,omitempty"`
	Paused         bool      `json:"paused,omitempty"`
	// PauseKind is the structured cause of Paused: manual_pause,
	// completion_standby or commander_standby. It is written by the control plane
	// (the project package does not import api), only meaningful while Paused, and
	// cleared on resume. Persisting it is what keeps a commander stop from coming
	// back as "the operator paused the project" after a restart (types.ts
	// project_pause_kind is the machine code the console classifies from).
	PauseKind string `json:"pause_kind,omitempty"`
	// MaxRuns overrides the instance --max-runs for this engagement. Nil means
	// "inherit the instance default"; a pointer is what distinguishes that from
	// 0, which is a deliberate "uncapped". It is a dispatch knob, not a fact:
	// changing it never touches the ledger.
	MaxRuns *int `json:"max_runs,omitempty"`
	// ReviewsDisabled is the operator's runtime review switch for this project.
	// A pointer distinguishes "never toggled" (follow the instance boot value)
	// from a deliberate false, so a restart does not re-close a review the
	// operator opened.
	ReviewsDisabled *bool `json:"reviews_disabled,omitempty"`
	// VerifyAuto is retired and kept only so an existing manifest still parses:
	// it is read, ignored and never rewritten with a new meaning. Reviewing is
	// fixed by PremisePolicy (verified_only): a direction's own conclusions are
	// reviewed at settlement and the pump serves cited premises, so there is no
	// instance value to inherit and no per-project switch. Nothing in the control
	// plane reads this field, and the settings endpoint rejects it rather than
	// storing a value that would look configurable and do nothing (D100 retired).
	VerifyAuto *bool `json:"verify_auto,omitempty"`
	// RoundTimeout overrides the instance --round-timeout for this engagement:
	// a Go duration ("15m", "90s"). Empty means "inherit the instance default";
	// "0" is a deliberate "no wall-clock cap". It bounds one commander decision
	// round, so a session that parks itself inside a round cannot hold every
	// later decision behind it (D91). ParseRoundTimeout is the decoder.
	RoundTimeout string `json:"round_timeout,omitempty"`
	// Nodes is the allowlist of execution locations this engagement may use.
	// A human verifies reachability on the node (terminal) and then binds it;
	// the decider never picks a location (docs/14-nodes.md).
	Nodes []string `json:"nodes,omitempty"`
	// DefaultNode is where intents without an explicit node run. It is always
	// one of Nodes, or empty when nothing is bound.
	DefaultNode string `json:"default_node,omitempty"`
	// NodesConfigured distinguishes "never bound" (false) from "deliberately
	// cleared" (true, with an empty Nodes). It lets a single-node instance
	// bind itself exactly once without overriding a human's choice to unbind.
	NodesConfigured bool `json:"nodes_configured,omitempty"`
	// Legacy model defaults are retained only to round-trip existing manifests.
	// They do not influence admission or routing.
	WorkerProfile string `json:"worker_profile,omitempty"`
	StrongProfile string `json:"strong_profile,omitempty"`
	// PeerPolicy governs who may message whom inside the project. Nil or an
	// empty mode means "open": every participant may message every other one,
	// which is the pre-policy behaviour and keeps old projects working. In
	// "allowlist" mode only the operator-listed (from,to) pairs may message;
	// the commander channel and a participant talking to itself are exempt
	// (the commander is the operator's channel, and self-messages are refused
	// before authorization is consulted).
	PeerPolicy *PeerPolicy `json:"peer_policy,omitempty"`
	// PremisePolicy is always "verified_only" (证明优先): every necessary premise
	// must be a reviewable kind, verified (a pass review bound to its current
	// claim version) with intact original evidence, before the direction can be
	// dispatched. Legacy standard/strict manifests are migrated to this value
	// when loaded and are no longer served as a read-only mode.
	PremisePolicy string `json:"premise_policy,omitempty"`
}

// Peer rule modes. Open is the default when the manifest has no policy.
const (
	PeerPolicyOpen      = "open"
	PeerPolicyAllowlist = "allowlist"
)

// PeerRule is one authorised direction of conversation: From may message To.
// Both are participant ids ("pt_in_x") or "commander"; the pair is directional.
type PeerRule struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// PeerPolicy is the operator's message-authorization rule set. Mode is open
// (default) or allowlist; Allow is only consulted in allowlist mode.
type PeerPolicy struct {
	Mode  string     `json:"mode,omitempty"`
	Allow []PeerRule `json:"allow,omitempty"`
}

// Project is one engagement. Dir is its root; everything engagement-scoped is
// below it, so deleting the directory deletes the project and nothing else.
type Project struct {
	Manifest
	Dir     string
	CaseDir string
	Store   *store.Store

	// mu guards the manifest fields the console updates while dispatchers and
	// pollers read them (node binding, scope, description).
	mu sync.RWMutex
	// writeMu serialises the manifest rewrite. It is separate from mu because
	// several callers already hold mu when they persist.
	writeMu sync.Mutex
}

// ScopeText and DescriptionText are the locked reads. The hub used to read the
// embedded fields directly, which raced with PUT /api/projects/{id}.
func (p *Project) ScopeText() string {
	if p.Store != nil {
		if doc, ok := p.Store.Document(store.DocumentScope); ok {
			return strings.TrimSpace(doc.Text)
		}
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.Scope
}

func (p *Project) IsPaused() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.Paused
}

// Pause is operator state, not a fact. Persist it with the manifest so a
// control-plane restart cannot silently resume an engagement and spend tokens.
//
// SetPaused keeps the existing structured cause: pausing is a transition, not a
// reclassification (a boot pause must not rewrite a persisted commander stop),
// and resuming always clears it. Callers that know the cause use
// SetPausedWithKind.
func (p *Project) SetPaused(paused bool) error {
	return p.SetPausedWithKind(paused, "")
}

// SetPausedWithKind persists the paused flag and its structured cause in one
// manifest write. An empty kind on pause keeps whatever the manifest already
// carries; a non-empty kind records the new cause; resume always clears it. A
// failed write rolls both fields back so memory and disk cannot disagree.
func (p *Project) SetPausedWithKind(paused bool, kind string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	before := p.Manifest
	p.Paused = paused
	switch {
	case !paused:
		p.PauseKind = ""
	case kind != "":
		p.PauseKind = kind
	}
	return p.writeOrRestore(func() { p.Paused, p.PauseKind = before.Paused, before.PauseKind })
}

// ReviewsDisabledOf returns the persisted review switch. ok=false means the
// operator has never toggled it, so the instance boot value applies.
func (p *Project) ReviewsDisabledOf() (bool, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.ReviewsDisabled == nil {
		return false, false
	}
	return *p.ReviewsDisabled, true
}

// SetReviewsDisabled persists the operator's review switch so a restart keeps
// the project's state instead of re-deriving it from the instance default.
func (p *Project) SetReviewsDisabled(disabled bool) error {
	v := disabled
	return p.setReviewsDisabled(&v)
}

// ClearReviewsDisabled drops the project's own value so the project follows the
// instance default again. It is the third state of the switch: distinct from a
// stored false, which means "this project reviews even if the instance default is
// closed".
func (p *Project) ClearReviewsDisabled() error {
	return p.setReviewsDisabled(nil)
}

func (p *Project) setReviewsDisabled(v *bool) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	before := p.Manifest
	p.ReviewsDisabled = v
	return p.writeOrRestore(func() { p.ReviewsDisabled = before.ReviewsDisabled })
}

// PauseKindOf returns the persisted pause cause ("" for a legacy pause or a
// running project). It is a machine code, never display prose.
func (p *Project) PauseKindOf() string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.PauseKind
}

func (p *Project) DescriptionText() string {
	if p.Store != nil {
		if doc, ok := p.Store.Document(store.DocumentConstraints); ok {
			return strings.TrimSpace(doc.Text)
		}
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.Description
}

// BriefRevisedAt is when the operator last changed the brief's text: the
// ScopeUpdatedAt stamp, or Created for a project that has never been revised.
// A zero brief time is reported as zero only when Created is zero too (a
// manifest written before Created existed); callers must then say "revision
// time unknown" instead of comparing it as if it were old.
func (p *Project) BriefRevisedAt() time.Time {
	p.mu.RLock()
	revision := p.ScopeUpdatedAt
	if revision.IsZero() {
		revision = p.Created
	}
	p.mu.RUnlock()
	if p.Store != nil {
		for _, doc := range p.Store.Documents() {
			if (doc.Kind == store.DocumentScope || doc.Kind == store.DocumentConstraints) && doc.UpdatedAt.After(revision) {
				revision = doc.UpdatedAt
			}
		}
	}
	return revision
}

// NodeBinding returns the project's node allowlist and default node.
func (p *Project) NodeBinding() ([]string, string) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return append([]string(nil), p.Nodes...), p.DefaultNode
}

// MaxRunsOverride reports the project's own concurrency cap. ok is false when
// the manifest has none, in which case dispatch uses the instance --max-runs.
func (p *Project) MaxRunsOverride() (int, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.MaxRuns == nil {
		return 0, false
	}
	return *p.MaxRuns, true
}

// VerifyAutoOverride is gone on purpose: nothing may derive a review policy
// from the retired verify_auto field. Reviewing is fixed by PremisePolicy, and
// the only legitimate reader of the legacy value is the JSON decoder above.

// RoundTimeoutRaw is the project's own commander round wall clock, as stored
// (a duration string). Empty means "inherit the instance --round-timeout";
// ParseRoundTimeout turns it into the duration the decider applies.
func (p *Project) RoundTimeoutRaw() string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.RoundTimeout
}

// PeerPolicyView returns the effective peer policy: an absent manifest policy
// is reported as open rather than as an empty mode, so callers (console, agent
// roster) can show what actually applies.
func (p *Project) PeerPolicyView() PeerPolicy {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return effectivePeerPolicy(p.PeerPolicy)
}

// PeerAllowed reports whether from may message to under the current policy. It
// is the one authorization rule the message path uses; policy edits and reads
// share the project lock, so a send cannot observe half an update.
func (p *Project) PeerAllowed(from, to string) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	pol := effectivePeerPolicy(p.PeerPolicy)
	if pol.Mode != PeerPolicyAllowlist {
		return true
	}
	for _, rule := range pol.Allow {
		if rule.From == from && rule.To == to {
			return true
		}
	}
	return false
}

// SetPeerPolicy validates and persists the operator's message-authorization
// rules. An empty mode is normalised to open; unknown modes and empty rule
// sides are errors rather than a silent downgrade (a typo must not quietly
// re-open or close the project's channels).
func (p *Project) SetPeerPolicy(pol PeerPolicy) (PeerPolicy, error) {
	normalized, err := normalizePeerPolicy(pol)
	if err != nil {
		return PeerPolicy{}, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	before := p.Manifest
	p.PeerPolicy = &normalized
	if err := p.writeOrRestore(func() { p.PeerPolicy = before.PeerPolicy }); err != nil {
		return PeerPolicy{}, err
	}
	return effectivePeerPolicy(p.PeerPolicy), nil
}

// effectivePeerPolicy fills in the open default. Caller holds p.mu (or owns the
// value, as at load time).
func effectivePeerPolicy(pol *PeerPolicy) PeerPolicy {
	if pol == nil {
		return PeerPolicy{Mode: PeerPolicyOpen}
	}
	out := PeerPolicy{Mode: strings.ToLower(strings.TrimSpace(pol.Mode)), Allow: append([]PeerRule(nil), pol.Allow...)}
	if out.Mode == "" {
		out.Mode = PeerPolicyOpen
	}
	return out
}

func normalizePeerPolicy(pol PeerPolicy) (PeerPolicy, error) {
	mode := strings.ToLower(strings.TrimSpace(pol.Mode))
	switch mode {
	case "":
		mode = PeerPolicyOpen
	case PeerPolicyOpen, PeerPolicyAllowlist:
	default:
		return PeerPolicy{}, fmt.Errorf("peer_policy.mode 只能是 open 或 allowlist（收到 %q）", pol.Mode)
	}
	seen := make(map[PeerRule]bool, len(pol.Allow))
	allow := make([]PeerRule, 0, len(pol.Allow))
	for _, rule := range pol.Allow {
		from, to := strings.TrimSpace(rule.From), strings.TrimSpace(rule.To)
		if from == "" || to == "" {
			return PeerPolicy{}, errors.New("peer_policy.allow 的 from 与 to 都必填")
		}
		if from == to {
			return PeerPolicy{}, fmt.Errorf("peer_policy.allow 不允许自己发给自己：%s", from)
		}
		clean := PeerRule{From: from, To: to}
		if seen[clean] {
			continue
		}
		seen[clean] = true
		allow = append(allow, clean)
	}
	return PeerPolicy{Mode: mode, Allow: allow}, nil
}

// SetNodeBinding replaces the allowlist. The empty list is legal and means
// "nothing can run here yet"; the default must be one of the bound nodes.
func (p *Project) SetNodeBinding(nodes []string, def string) error {
	seen := make(map[string]bool, len(nodes))
	clean := make([]string, 0, len(nodes))
	for _, id := range nodes {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		clean = append(clean, id)
	}
	def = strings.TrimSpace(def)
	if def != "" && !seen[def] {
		return errors.New("默认节点必须在绑定列表里")
	}
	if len(clean) > 0 && def == "" {
		return errors.New("绑定了节点就要选一个默认节点")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	before := p.Manifest
	p.Nodes, p.DefaultNode, p.NodesConfigured = clean, def, true
	return p.writeOrRestore(func() {
		p.Nodes, p.DefaultNode, p.NodesConfigured = before.Nodes, before.DefaultNode, before.NodesConfigured
	})
}

// BindingConfigured reports whether a human (or the single-node auto-bind) has
// written a binding. An unconfigured project is eligible for the one-machine
// default; a configured empty one is a deliberate "nothing may run here".
func (p *Project) BindingConfigured() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.NodesConfigured
}

func (p *Project) LedgerPath() string    { return filepath.Join(p.Dir, "ledger.sqlite") }
func (p *Project) StatePath() string     { return filepath.Join(p.Dir, "state.md") }
func (p *Project) DecisionsPath() string { return filepath.Join(p.Dir, "decisions.jsonl") }
func (p *Project) RunsDir() string       { return filepath.Join(p.Dir, "runs") }

// Manager owns the open projects of one instance. It never opens the same
// ledger twice: two Store values over one file would overwrite each other.
type Manager struct {
	Root string

	// pooled selects the ledger handle mode for every project this manager
	// owns. A long-lived runtime keeps one idle SQLite connection per project so
	// writes do not reopen the database per commit (store.OpenSQLitePooled); its
	// delete path closes the ledger before removing the directory. The zero
	// value is the transient mode, which fixtures and one-shot tools want.
	pooled bool

	// skipped keeps the directories that could not be loaded. Opening the
	// instance survives one broken engagement, but the operator has to be able
	// to see which one it was and why (startup log + console warning).
	skipped []SkippedProject

	mu   sync.RWMutex
	byID map[string]*Project
	ids  []string
}

// SkippedProject is one project directory the instance could not load.
//
// The directory is left exactly as it is: repairing or removing it is the
// operator's call, and the load path never writes a placeholder ledger or any
// other structure into it. Err is the original load error, verbatim, so the
// console can say why the engagement is missing instead of just that it is.
type SkippedProject struct {
	ID  string `json:"id"`
	Dir string `json:"dir"`
	Err string `json:"error"`
}

// Skipped returns the project directories this manager could not load, in
// directory order. A runtime exposes them so "a project is missing" is visible
// instead of looking like an ordinary short list.
func (m *Manager) Skipped() []SkippedProject {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return append([]SkippedProject(nil), m.skipped...)
}

// SkippedProject returns the load failure recorded for one directory id.
func (m *Manager) SkippedProject(id string) (SkippedProject, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, s := range m.skipped {
		if s.ID == id {
			return s, true
		}
	}
	return SkippedProject{}, false
}

// Open loads every project directory below root, creating root if needed.
// A directory without a manifest is adopted: hand-made directories are a
// legitimate way to restore an engagement.
//
// Its ledgers keep no idle handle: removing a project directory does not
// require closing the store first. Use OpenPooled for a project runtime.
func Open(root string) (*Manager, error) { return openManager(root, false) }

// OpenPooled loads the projects the way a runtime uses them: each ledger keeps
// one idle connection (store.OpenSQLitePooled), which removes the per-commit
// handle cost from every write. The price is one open OS handle per project, so
// a caller that deletes a project directory must Close its store first —
// cmd/saker/hub.go deleteProject does. Nothing may rely on the garbage collector.
func OpenPooled(root string) (*Manager, error) { return openManager(root, true) }

func openManager(root string, pooled bool) (*Manager, error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	ents, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	m := &Manager{Root: root, pooled: pooled, byID: map[string]*Project{}}
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		p, err := load(root, e.Name(), pooled)
		if err != nil {
			// One engagement that cannot be opened must not stop the instance:
			// the console and every other project keep working. The directory is
			// reported, never repaired here — deleting it, or writing a fresh
			// ledger into it, would turn a recoverable load error into data loss.
			dir := filepath.Join(root, e.Name())
			log.Printf("project %s 未加载（%s）：%v；目录保留原样，其它项目照常启动", e.Name(), dir, err)
			m.skipped = append(m.skipped, SkippedProject{ID: e.Name(), Dir: dir, Err: err.Error()})
			continue
		}
		m.byID[p.ID] = p
		m.ids = append(m.ids, p.ID)
	}
	m.sort()
	return m, nil
}

// openLedger opens one project ledger in the manager's handle mode.
func openLedger(path string, pooled bool) (*store.Store, error) {
	if pooled {
		return store.OpenSQLitePooled(path)
	}
	return store.OpenSQLite(path)
}

func load(root, dir string, pooled bool) (*Project, error) {
	d := filepath.Join(root, dir)
	if _, err := os.Stat(filepath.Join(d, "ledger.sqlite")); err != nil {
		return nil, fmt.Errorf("project %s requires a current SQLite ledger: %w", dir, err)
	}
	p := &Project{Dir: d, CaseDir: filepath.Join(d, "cases")}
	migrated := false
	if b, err := os.ReadFile(filepath.Join(d, ManifestFile)); err == nil {
		if err := json.Unmarshal(b, &p.Manifest); err != nil {
			return nil, fmt.Errorf("parse %s: %w", d, err)
		}
	}
	// One-way, one-time migration: standard/strict/no policy all mean
	// verified_only now. The old value is not served as a compatibility mode;
	// writing it here makes the migration explicit in the manifest and keeps the
	// in-memory project consistent with disk before any dispatch.
	if strings.TrimSpace(p.PremisePolicy) != PremisePolicyVerifiedOnly {
		p.PremisePolicy = PremisePolicyVerifiedOnly
		migrated = true
	}
	// The directory name is the address the console uses; trust it over a
	// manifest that may have been copied from another project.
	p.ID = dir
	if p.Name == "" {
		p.Name = p.ID
	}
	if p.Created.IsZero() {
		if info, err := os.Stat(filepath.Join(d, "ledger.sqlite")); err == nil {
			p.Created = info.ModTime()
		} else {
			p.Created = time.Now()
		}
	}
	st, err := openLedger(p.LedgerPath(), pooled)
	if err != nil {
		return nil, err
	}
	p.Store = st
	// Case structure is created only after the ledger opened. A directory whose
	// ledger cannot be opened is reported as-is: the load path must not leave
	// half-made structure (or a placeholder ledger) behind in it, because that is
	// exactly what makes the next attempt look like a healthy empty engagement.
	if err := os.MkdirAll(p.CaseDir, 0o755); err != nil {
		_ = st.Close()
		return nil, err
	}
	if _, err := os.Stat(filepath.Join(d, ManifestFile)); os.IsNotExist(err) {
		_ = p.write()
	} else if migrated {
		if err := p.write(); err != nil {
			_ = st.Close()
			return nil, fmt.Errorf("migrate %s to verified_only: %w", d, err)
		}
		log.Printf("project %s: premise_policy migrated to verified_only", p.ID)
	}
	return p, nil
}

func (m *Manager) sort() {
	sort.SliceStable(m.ids, func(i, j int) bool {
		a, b := m.byID[m.ids[i]], m.byID[m.ids[j]]
		if !a.Created.Equal(b.Created) {
			return a.Created.Before(b.Created)
		}
		return a.ID < b.ID
	})
}

// All returns the projects in creation order.
func (m *Manager) All() []*Project {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*Project, 0, len(m.ids))
	for _, id := range m.ids {
		out = append(out, m.byID[id])
	}
	return out
}

func (m *Manager) Get(id string) *Project {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.byID[id]
}

func (m *Manager) Len() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.ids)
}

// First returns the project the console opens on / when --project is unset.
func (m *Manager) First() *Project {
	all := m.All()
	if len(all) == 0 {
		return nil
	}
	return all[0]
}

// Create makes a new engagement. The directory id is a short random id,
// independent of the display name: renaming the engagement never moves its
// directory, and a name/brief naming a target host or URL does not leak into the
// on-disk path or the console URL. The readable name lives in the manifest.
func (m *Manager) Create(name, scope string) (*Project, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("项目名不能为空")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.create(newProjectID(), name, scope)
}

// newProjectID is the stable, opaque directory address: "p-" plus six base32
// characters (30 random bits). Short enough to read in a URL, and the name never
// enters the path. Uniqueness is not promised by the random part alone:
// Manager.uniqueID checks the in-memory map and the directory before anything is
// written, and retries on a collision.
func newProjectID() string {
	return "p-" + store.NewCode(6)
}

// create must be called with the write lock held.
func (m *Manager) create(base, name, scope string) (*Project, error) {
	id := m.uniqueID(base)
	d := filepath.Join(m.Root, id)
	if err := os.MkdirAll(filepath.Join(d, "cases"), 0o755); err != nil {
		return nil, err
	}
	p := &Project{
		Manifest: Manifest{ID: id, Name: name, Scope: strings.TrimSpace(scope), Created: time.Now(), PremisePolicy: PremisePolicyVerifiedOnly},
		Dir:      d,
		CaseDir:  filepath.Join(d, "cases"),
	}
	st, err := openLedger(filepath.Join(p.Dir, "ledger.sqlite"), m.pooled)
	if err != nil {
		return nil, err
	}
	p.Store = st
	if err := p.write(); err != nil {
		return nil, err
	}
	m.byID[id] = p
	m.ids = append(m.ids, id)
	m.sort()
	return p, nil
}

func (m *Manager) uniqueID(base string) string {
	if base == "" {
		base = "prj"
	}
	if _, taken := m.byID[base]; !taken {
		if _, err := os.Stat(filepath.Join(m.Root, base)); os.IsNotExist(err) {
			return base
		}
	}
	for i := 0; i < 64; i++ {
		id := base + "-" + store.NewCode(6)
		if _, taken := m.byID[id]; taken {
			continue
		}
		if _, err := os.Stat(filepath.Join(m.Root, id)); os.IsNotExist(err) {
			return id
		}
	}
	return base + "-" + store.NewCode(6)
}

// Adopt opens a project directory that appeared after start (an imported
// project) and registers it. It is the runtime equivalent of the load loop in
// Open: the directory must already contain a manifest and a current ledger, and
// an id already in memory is refused rather than opened twice.
func (m *Manager) Adopt(id string) (*Project, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.byID[id]; ok {
		return nil, fmt.Errorf("项目已打开：%s", id)
	}
	p, err := load(m.Root, id, m.pooled)
	if err != nil {
		return nil, err
	}
	m.byID[p.ID] = p
	m.ids = append(m.ids, p.ID)
	m.sort()
	return p, nil
}

// Remove forgets a project whose directory is already gone. It does not touch
// the disk: deleting the tree is the caller's job, because only the caller
// knows whether a run is still writing into it.
func (m *Manager) Remove(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.byID[id]; !ok {
		return
	}
	delete(m.byID, id)
	for i, v := range m.ids {
		if v == id {
			m.ids = append(m.ids[:i], m.ids[i+1:]...)
			break
		}
	}
}

// SetScope rewrites the authorised scope of an engagement without touching the
// ledger: scope is a property of the engagement, not a fact about it.
func (m *Manager) SetScope(id, scope string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.byID[id]
	if !ok {
		return fmt.Errorf("没有这个项目：%s", id)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	before := p.Manifest
	p.Scope = strings.TrimSpace(scope)
	// A settings card submits every field, so the same text arrives again and
	// again: an unconditional stamp would keep pushing the brief into the future
	// and let it silently outrank every authorization the operator issued before
	// the last save.
	if p.Scope != before.Scope {
		p.ScopeUpdatedAt = time.Now().UTC()
	}
	return p.writeOrRestore(func() { p.Manifest = before })
}

// SetDescription writes the operator's prose for this engagement. The program
// never parses it; it only carries it into every brief (D59).
func (m *Manager) SetDescription(id, description string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.byID[id]
	if !ok {
		return fmt.Errorf("没有这个项目：%s", id)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	before := p.Manifest
	p.Description = strings.TrimSpace(description)
	if p.Description != before.Description {
		p.ScopeUpdatedAt = time.Now().UTC()
	}
	return p.writeOrRestore(func() { p.Manifest = before })
}

// BriefRevision is the timestamp a reader compares against the newest
// applicable instruction: "" (never revised) falls back to project creation, and
// a project with neither is reported as unknown rather than as year 1.
func (p *Project) BriefRevision() time.Time {
	return p.BriefRevisedAt()
}

// SetMaxRuns writes the project's own concurrency cap; nil restores "inherit
// the instance default". 0 is legal and means uncapped. The value is copied so
// the caller cannot mutate the manifest after the write.
func (m *Manager) SetMaxRuns(id string, limit *int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.byID[id]
	if !ok {
		return fmt.Errorf("没有这个项目：%s", id)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	before := p.Manifest
	if limit == nil {
		p.MaxRuns = nil
	} else {
		v := *limit
		p.MaxRuns = &v
	}
	return p.writeOrRestore(func() { p.MaxRuns = before.MaxRuns })
}

// SetVerifyAuto is gone on purpose: the control plane must not offer a review
// switch it cannot honour. A legacy manifest value stays where it is (parsed,
// ignored, not migrated), and no endpoint may rewrite it into a live setting.

// ParseRoundTimeout decodes a manifest / console round wall-clock value. Empty
// means "inherit the instance default" (ok=false). "0" is a deliberate
// unlimited (ok=true, zero duration). Malformed and negative values are errors:
// a typo must not silently become "inherit".
func ParseRoundTimeout(raw string) (time.Duration, bool, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return 0, false, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, false, fmt.Errorf("round_timeout 不是合法时长（如 15m / 90s / 0）：%q", s)
	}
	if d < 0 {
		return 0, false, errors.New("round_timeout 不能是负数（0 = 不限）")
	}
	return d, true, nil
}

// SetRoundTimeout writes the project's own commander round wall clock; "" clears
// it and restores "inherit the instance --round-timeout", "0" means unlimited.
// The value is validated here so an invalid file cannot reach the decider
// through the console.
func (m *Manager) SetRoundTimeout(id, raw string) error {
	s := strings.TrimSpace(raw)
	if _, _, err := ParseRoundTimeout(s); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.byID[id]
	if !ok {
		return fmt.Errorf("没有这个项目：%s", id)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	before := p.Manifest
	p.RoundTimeout = s
	return p.writeOrRestore(func() { p.RoundTimeout = before.RoundTimeout })
}

// writeOrRestore keeps failed edits out of live policy and future saves. The
// caller holds p.mu and restores only the fields it changed: replacing the whole
// manifest would also rewrite immutable identity fields read without that lock.
func (p *Project) writeOrRestore(restore func()) error {
	if err := p.write(); err != nil {
		restore()
		return err
	}
	return nil
}

func (p *Project) write() error {
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	b, err := json.MarshalIndent(&p.Manifest, "", "  ")
	if err != nil {
		return err
	}
	// Atomic replace: two saves (scope vs node binding) used to race on a plain
	// WriteFile, and a half-written project.json makes the whole instance fail
	// to start on the next boot.
	path := filepath.Join(p.Dir, ManifestFile)
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(b, '\n')); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
