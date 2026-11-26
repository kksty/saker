package platform

// Isolation capabilities are declared here once and served read-only. The
// declaration is deliberately identical for Windows and Linux: neither platform
// enforces process or network isolation for the model's tools. The harness and
// the console's command execution run as the operator's own account, so a
// sandbox cannot be claimed; what the project can express is the limit itself.
//
// These entries are the machine-readable form of the "已知限制" section in
// docs/01-architecture.md. They are not a policy input: no code may grant
// authority because a boundary is absent.
const (
	// StatusUnsupportedBoundary means the capability is not implemented and the
	// platform must not describe it as enforced.
	StatusUnsupportedBoundary = "unsupported_boundary"
)

// Boundary is one isolation capability and what the platform actually does.
type Boundary struct {
	// ID is stable for machine consumers (process, network, filesystem, ...).
	ID string `json:"id"`
	// Capability is the human name of the control.
	Capability string `json:"capability"`
	// Status is StatusUnsupportedBoundary for every current capability, on every
	// platform. A future enforced capability gets a different status.
	Status string `json:"status"`
	// Enforced is false everywhere: no process/network isolation is applied.
	Enforced bool `json:"enforced"`
	// Detail explains the limitation in one sentence (Chinese: this is
	// operator-facing text).
	Detail string `json:"detail"`
}

// IsolationBoundaries returns the isolation declaration for one target GOOS.
// The list is the same for Windows and Linux; the platform argument exists so
// tests can prove the two declarations agree instead of trusting a comment.
func IsolationBoundaries(goos string) []Boundary {
	family := "POSIX"
	if IsWindows(goos) {
		family = "Windows"
	}
	return []Boundary{
		{
			ID:         "process",
			Capability: "进程隔离",
			Status:     StatusUnsupportedBoundary,
			Enforced:   false,
			Detail:     "执行者工具与宿主子进程以操作员账户运行，" + family + " 上均不提供进程或文件系统沙箱；不能依靠平台阻止越权命令或 PTY 逃逸。",
		},
		{
			ID:         "network",
			Capability: "网络隔离",
			Status:     StatusUnsupportedBoundary,
			Enforced:   false,
			Detail:     "平台不拦截出网或入网，" + family + " 上均不提供网络强制隔离；授权范围是记录与提示，不是逐请求过滤器。",
		},
		{
			ID:         "filesystem",
			Capability: "文件系统边界",
			Status:     StatusUnsupportedBoundary,
			Enforced:   false,
			Detail:     "证据目录与项目工作区是同账户普通文件，" + family + " 上均不宣称 ACL/挂载级隔离；敏感数据需部署方自行限制账户与网络。",
		},
		{
			ID:         "credentials",
			Capability: "凭据隔离",
			Status:     StatusUnsupportedBoundary,
			Enforced:   false,
			Detail:     "控制台登录令牌具备本机执行权限；" + family + " 上均不提供凭据沙箱，应按主机凭据保护。",
		},
	}
}

// DocumentedLimitations returns the prose known limitations for goos. They match
// the architecture document and are stable strings so the API can serve them
// without copying text at each call site.
func DocumentedLimitations(goos string) []string {
	family := "Linux"
	if IsWindows(goos) {
		family = "Windows"
	}
	return []string{
		family + " 与 Linux 同等支持：两端都可构建、运行并执行同一套本地检查。",
		"进程与网络隔离在 " + family + " 与 Linux 上均未实现，状态为 " + StatusUnsupportedBoundary + "。",
		"授权资产范围不是逐请求的强制过滤器，平台只记录与展示授权边界。",
		"控制台令牌与本机账户同权限，模型工具不受平台提供的文件系统沙箱保护。",
	}
}

// BoundaryReport is the read-only status document served by the API: the
// platform descriptor, every isolation declaration and the documented limits.
type BoundaryReport struct {
	Schema    string     `json:"schema"`
	Platform  Descriptor `json:"platform"`
	Isolation []Boundary `json:"isolation"`
	// Limitations are prose for the operator; they are documentation, not
	// control input.
	Limitations []string `json:"documented_limitations"`
}

// BoundarySchema versions the response shape. It is a read-only status document
// and is not part of the ledger schema.
const BoundarySchema = "saker.platform-boundary.v1"

// Report builds the boundary document for one target platform.
func Report(goos, goarch string) BoundaryReport {
	return BoundaryReport{
		Schema:      BoundarySchema,
		Platform:    Describe(goos, goarch),
		Isolation:   IsolationBoundaries(goos),
		Limitations: DocumentedLimitations(goos),
	}
}
