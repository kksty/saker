// Package buildinfo collects the build manifest of one saker checkout.
//
// A verification result is only as useful as its answer to "which source and
// which binaries produced this?". The manifest records exactly that (taskbook
// P0-1): source identity (commit, branch, working-tree patch digest, submodule),
// harness identity and binary digests, the control-plane binaries, the protocol
// and data-format versions this build speaks, the fingerprint of the vendored
// AOP generated code (and whether it still matches the harness source), the
// platform/toolchain it ran on, and the model connection it was configured
// against — including the model id the upstream endpoint actually returned,
// which the historic gateway aggregate name could not tell apart.
//
// The manifest is a local artifact under .build/ (git-ignored): it records the
// absolute checkout paths so the same machine can retrace them, and it never
// records a credential value. API keys only appear as a boolean.
package buildinfo

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kksty/saker/internal/board"
	"github.com/kksty/saker/internal/bundle"
	"github.com/kksty/saker/internal/ioa"
	"github.com/kksty/saker/internal/store"
)

// ManifestVersion is the JSON layout version. Bump when an old reader would
// misread a new manifest.
const ManifestVersion = 1

// ManifestPath is the default output. .build/ is git-ignored on purpose: the
// manifest names this machine and must not be committed.
const ManifestPath = ".build/build-manifest.json"

// Manifest is the complete build manifest.
type Manifest struct {
	ManifestVersion int             `json:"manifest_version"`
	GeneratedAt     string          `json:"generated_at"` // UTC RFC3339
	Source          SourceIdentity  `json:"source"`
	Harness         HarnessIdentity `json:"harness"`
	// AOPProto is the vendored AOP generated-code fingerprint and the drift
	// check result against the harness source (see aopproto.go). It is recorded
	// even when no harness checkout is available: the manifest must identify the
	// protocol bytes that were built either way.
	AOPProto     *AOPProtoInfo `json:"aop_proto,omitempty"`
	ControlPlane ControlPlane  `json:"control_plane"`
	Versions     Versions      `json:"versions"`
	Platform     Platform      `json:"platform"`
	Model        ModelInfo     `json:"model"`
	Gate         *GateInfo     `json:"gate,omitempty"`
}

// SourceIdentity is the saker checkout the checks ran against.
type SourceIdentity struct {
	// Root is the absolute checkout path. Paths in this file are slash-separated
	// so a Windows and a Linux manifest can be compared line by line.
	Root        string          `json:"root"`
	Commit      string          `json:"commit"`
	Branch      string          `json:"branch"`
	Subject     string          `json:"subject"`
	Dirty       bool            `json:"dirty"`
	PatchSHA256 string          `json:"patch_sha256"`
	Untracked   []UntrackedFile `json:"untracked"`
	Submodules  []Submodule     `json:"submodules"`
	// Status is the porcelain status at manifest time (with core.fileMode=false,
	// so a /mnt/c checkout does not report the whole tree as modified).
	Status []string `json:"status"`
	// SubmoduleError explains a failed `git submodule status` without failing
	// the manifest: a missing submodule is a fact to record, not a crash.
	SubmoduleError *string `json:"submodule_error,omitempty"`
}

// UntrackedFile is one untracked, non-ignored file folded into PatchSHA256.
type UntrackedFile struct {
	Path   string `json:"path"`
	Kind   string `json:"kind"` // "file" or "symlink"
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// Submodule is one gitlink of the source checkout.
type Submodule struct {
	Path         string `json:"path"`
	Commit       string `json:"commit"` // checked-out commit when initialized, else the pinned one
	PinnedCommit string `json:"pinned_commit"`
	Initialized  bool   `json:"initialized"`
}

// HarnessIdentity is the independent cyber-harness checkout and what was built
// from it.
type HarnessIdentity struct {
	SourceDir     string   `json:"source_dir"`
	SourceMissing bool     `json:"source_missing,omitempty"`
	Commit        *string  `json:"commit"`
	Branch        *string  `json:"branch"`
	Subject       *string  `json:"subject"`
	Dirty         *bool    `json:"dirty"`
	SourceError   *string  `json:"source_error,omitempty"`
	Binaries      []Binary `json:"binaries"`
}

// ControlPlane names the saker and saker-repair binaries built by the gate.
type ControlPlane struct {
	BinDir   string   `json:"bin_dir"`
	Binaries []Binary `json:"binaries"`
}

// Binary is one executable the manifest vouches for.
type Binary struct {
	Name string `json:"name"`
	// Path is repository-relative when the binary lives inside the checkout,
	// otherwise absolute (slash-separated either way).
	Path            string  `json:"path"`
	Size            int64   `json:"size"`
	SHA256          string  `json:"sha256"`
	Missing         bool    `json:"missing,omitempty"`
	Version         string  `json:"version"`                     // stdout+stderr of --version
	VersionError    *string `json:"version_error"`               // null when --version exited 0
	VersionExitCode *int    `json:"version_exit_code,omitempty"` // null when the binary is missing
}

// Versions are the wire and data-format versions this build speaks. The values
// are imported from the defining packages (not copied) so a schema bump cannot
// leave the manifest lying.
type Versions struct {
	IOAProtocol   int `json:"ioa_protocol"`
	StoreSchema   int `json:"store_schema"`
	Bundle        int `json:"bundle"`
	BundleProject int `json:"bundle_project"`
	Graph         int `json:"graph"`
}

// Platform is the target and toolchain the checks ran on. A tool that is not
// installed is recorded as JSON null plus an entry in Errors; it never fails the
// manifest by itself (taskbook P0-1: "node/pnpm 缺失记 null 而不是失败").
type Platform struct {
	GOOS        string            `json:"goos"`
	GOARCH      string            `json:"goarch"`
	GOVersion   string            `json:"go_version"`
	NodeVersion *string           `json:"node_version"`
	PNPMVersion *string           `json:"pnpm_version"`
	GitVersion  *string           `json:"git_version"`
	Errors      map[string]string `json:"errors,omitempty"`
}

// ModelInfo is the model connection the checkout was configured against. The
// API key is never written; only whether one is set.
type ModelInfo struct {
	ConfigPath    string     `json:"config_path"`
	ConfigPresent bool       `json:"config_present"`
	ActiveProfile *string    `json:"active_profile"`
	Provider      *string    `json:"provider"`
	Protocol      *string    `json:"protocol"` // resolved wire protocol: openai | anthropic
	ModelID       *string    `json:"model_id"` // configured model id
	BaseURL       *string    `json:"base_url"` // scheme://host only (no userinfo, path, query)
	BaseURLPath   *string    `json:"base_url_path,omitempty"`
	APIKeyPresent bool       `json:"api_key_present"`
	Probe         *ProbeInfo `json:"probe,omitempty"`
	Error         *string    `json:"error,omitempty"` // malformed config, in human words
}

// ProbeInfo is one minimal chat/completions request against the active provider.
// It records the model id the response actually carried, which is the whole
// point: a gateway aggregate name cannot be attributed to a real upstream model.
type ProbeInfo struct {
	Probed        bool    `json:"probed"`
	Available     bool    `json:"available"`
	Reason        *string `json:"reason"` // why the probe did not run (nil when it did)
	ReturnedModel *string `json:"returned_model"`
	// UpstreamModel / UpstreamProvider are the real attribution when the gateway
	// in front of this connection reports it in response headers; without it the
	// body's model field can only echo the aggregate alias.
	UpstreamModel    *string `json:"upstream_model"`
	UpstreamProvider *string `json:"upstream_provider"`
	HTTPStatus       *int    `json:"http_status"`
	FinishReason     *string `json:"finish_reason,omitempty"`
	LatencyMS        int64   `json:"latency_ms"`
	Error            *string `json:"error"` // redacted; never contains the API key
}

// GateInfo is filled by cmd/saker-gate and absent for a plain cmd/saker-manifest run.
type GateInfo struct {
	StartedAt  string             `json:"started_at"`
	FinishedAt string             `json:"finished_at"`
	Skips      Skips              `json:"skips"`
	Checks     []Check            `json:"checks"`
	Embedded   *EmbeddedArtifacts `json:"embedded_artifacts,omitempty"`
}

// Skips names the explicitly skipped check groups.
type Skips struct {
	Web     bool `json:"web"`
	Harness bool `json:"harness"`
}

// Check is one gate step and how long it took.
type Check struct {
	Name       string `json:"name"`
	DurationMS int64  `json:"duration_ms"`
	// Skipped marks an optional check that could not run (for example the AOP
	// drift check without a harness checkout). A skipped check is a recorded
	// fact, not a failure, and the run reports it in the gate log.
	Skipped    bool   `json:"skipped,omitempty"`
	SkipReason string `json:"skip_reason,omitempty"`
}

// EmbeddedArtifacts is the freshness comparison around the frontend build.
// Changed is empty on success; a non-empty list is a gate failure: the embedded
// internal/web/static was behind the web sources and the rebuild only now
// produced the difference.
type EmbeddedArtifacts struct {
	Dir          string   `json:"dir"`
	BeforeStatus []string `json:"before_status"`
	AfterStatus  []string `json:"after_status"`
	BeforeSHA256 string   `json:"before_sha256"`
	AfterSHA256  string   `json:"after_sha256"`
	Changed      []string `json:"changed"`
}

// Options configures one manifest collection.
type Options struct {
	// Root is the saker checkout root (required).
	Root string
	// HarnessDir is the resolved cyber-harness source directory, "" when absent
	// or skipped.
	HarnessDir string
	// BinDir is where the gate built/will build binaries (relative to Root).
	BinDir string
	// HarnessConfig overrides the harness config path; "" means
	// $SAKER_HARNESS_CONFIG, then ~/.cyber/cyber.yaml.
	HarnessConfig string
	// ProbeModel sends the one minimal request that records the real model id.
	ProbeModel bool
	// ProbeTimeout bounds that request (default 30s).
	ProbeTimeout time.Duration
	// AOPProto is the already computed vendored AOP drift result. The gate runs
	// the check as a step and passes the result here so the manifest and the
	// check log cannot disagree; when nil Collect runs the check itself
	// (cmd/saker-manifest and -manifest-only).
	AOPProto *AOPProtoInfo
	// Gate is set by cmd/saker-gate; nil for cmd/saker-manifest.
	Gate *GateInfo
}

// Collect builds the manifest. It fails only when the source identity cannot be
// trusted (not a git checkout, git missing); missing optional tools, harness
// sources or binaries are recorded as facts instead of aborting.
func Collect(ctx context.Context, opts Options) (*Manifest, error) {
	if strings.TrimSpace(opts.Root) == "" {
		return nil, fmt.Errorf("缺少仓库根目录")
	}
	root, err := filepath.Abs(opts.Root)
	if err != nil {
		return nil, fmt.Errorf("解析仓库根目录：%w", err)
	}
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		return nil, fmt.Errorf("仓库根目录不可用：%s", root)
	}

	source, err := collectSource(root)
	if err != nil {
		return nil, err
	}

	binDir := opts.BinDir
	if strings.TrimSpace(binDir) == "" {
		binDir = "bin"
	}
	goos, goarch := targetPlatform()
	exe := executableSuffix(goos)

	aopProto := opts.AOPProto
	if aopProto == nil {
		aopProto = CheckAOPProto(root, opts.HarnessDir)
	}

	m := &Manifest{
		ManifestVersion: ManifestVersion,
		GeneratedAt:     time.Now().UTC().Format(time.RFC3339),
		Source:          source,
		Harness:         collectHarness(root, opts.HarnessDir, binDir, exe),
		AOPProto:        aopProto,
		ControlPlane: ControlPlane{
			BinDir: manifestPath(root, binDir),
			Binaries: []Binary{
				collectBinary(root, "saker", filepath.Join(binDir, "saker"+exe)),
				collectBinary(root, "saker-repair", filepath.Join(binDir, "saker-repair"+exe)),
			},
		},
		Versions: Versions{
			IOAProtocol:   ioa.ProtocolVersion,
			StoreSchema:   store.CurrentSchemaVersion(),
			Bundle:        bundle.Version,
			BundleProject: bundle.ProjectVersion,
			Graph:         board.GraphVersion,
		},
		Platform: collectPlatform(goos, goarch),
		Model:    collectModel(ctx, root, opts),
		Gate:     opts.Gate,
	}
	if m.Gate != nil {
		m.Gate.FinishedAt = time.Now().UTC().Format(time.RFC3339)
	}
	return m, nil
}

// Write writes the manifest as indented JSON. The parent directory is created
// because .build/ is usually absent in a fresh checkout.
func Write(path string, m *Manifest) error {
	return writeManifest(path, m)
}

// ManifestRelPath turns the manifest output path into a repository-relative
// slash path for messages, leaving it absolute when it is outside the root.
func ManifestRelPath(root, path string) string {
	return manifestPath(root, path)
}
