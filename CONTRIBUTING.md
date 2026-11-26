# 贡献指南

欢迎提交问题、文档修正与代码贡献。参与前请阅读 [安全策略](SECURITY.md)、[核心架构](docs/01-architecture.md)与[设计取向](AGENTS.md)。

## 授权与内容来源

只提交虚构目标、假凭据和脱敏材料。不要提交客户信息、真实目标、证据、API key、登录令牌或本地配置。第三方内容需说明来源并保留许可证。贡献本项目即表示你有权提交这些内容，并同意按 [AGPL-3.0](LICENSE) 发布；第三方资产遵循其原有许可，见 [NOTICE](NOTICE)。

## 开发环境与检查

Windows 与 Linux 是同等支持的开发与验收平台：两平台都需要 Go 1.26+、Node.js 24+、pnpm 与 Git，并且都能完整跑通同一套本地 gate。真实实例的验收记录目前只有 Windows：Linux 上 gate、测试与构建可跑，端到端实例行为尚未做过验收。Windows 执行宿主还需要 Git for Windows 的 Bash 与 POSIX 工具。安装前端依赖（每个平台各自执行一次 `pnpm install`，原生依赖按平台安装；控制台组件库在主仓 `web/packages` 里，没有子模块要初始化）：

```bash
pnpm install
```

提交前在仓库根目录运行本地 gate。项目当前没有持续集成，用本地 gate 替代；检查只有一份实现（`cmd/saker-gate`），两个脚本只负责定位仓库根并透传参数和退出码，两平台检查项完全相同：

Windows / PowerShell：

```powershell
pwsh -File scripts/gate.ps1
```

Linux / macOS：

```bash
./scripts/gate.sh      # 或 bash scripts/gate.sh
```

`make gate` 等价。gate 串行执行并逐步打印耗时：`go build ./cmd/... ./internal/...`、`go vet ./cmd/... ./internal/...`、`go test ./cmd/... ./internal/... -count=1`、前端 `pnpm --dir web typecheck` / `test` / `build`、嵌入产物新鲜度、`node scripts/check-docs.mjs`、AOP 生成代码漂移检查，再从 harness 源码构建 `cyber-harness` + `cyber-scan`、构建 `bin/saker` 与 `bin/saker-repair`，最后写构建清单 `.build/build-manifest.json`（字段说明见 [README](README.md#构建清单)）。Go 命令刻意不用 `./...`：默认数据目录 `./data` 在模块内，一次真实测试会把目标源码落到 `data/projects/<id>/cases/…`，Go 会把它们当包，`./...` 于是在跑过项目的机器上必红。仓库自己的包都在 `cmd/` 与 `internal/` 下，搬到别处时 `cmd/saker-gate` 的覆盖率测试会失败。缺依赖时 gate 明确失败并给出两平台安装指引，不会静默跳过；需要部分检查时用显式开关（`-skip-web`、`-skip-harness`、`-manifest-only`、`-no-model-probe`），不要手工删减检查。Linux 上用发行版包或官方 tarball 安装 Node.js 24+ 后安装 pnpm，再按上面的双平台命令验证（前端测试直接执行 `.ts`，需要 Node 编译时启用 TypeScript 支持：`node -p "process.config.variables.node_use_amaro"` 应为 `true`，否则改用官方 Node 二进制包）；在 WSL 中直接跑 Windows 上的 checkout 时先执行 `git config --global core.autocrlf true`（与 Git for Windows 默认一致），否则 git 会把整棵树当作已修改。

gate 默认发一次 `max_tokens=1` 的模型请求以记录真实 model id；模型配置来自 `--harness-config` / `SAKER_HARNESS_CONFIG` / `~/.cyber/cyber.yaml`，无配置或探测失败只记录原因，不使 gate 失败，API key 不写入清单。

前端测试包含纯函数和真实 React 组件的状态回归，不需要运行控制面或调用模型。`internal/web/static` 是入库的嵌入产物，前端源码修改后必须重新构建并一并提交；gate 在构建前后比较该目录，只有本次重建新产生的差异才算失败（工作树原本就脏不算失败）。资源不足时可用 `go test -p 1 ./cmd/... ./internal/...`。并发敏感改动（复核结算、调度/唤醒、store 事务、decider）还应在有 C 工具链的环境跑 race 检测（Go race detector，需要 `CGO_ENABLED=1` 与 gcc）：Windows 用 `powershell -ExecutionPolicy Bypass -File scripts/race.ps1`，git-bash/WSL 用 `make race`，两者跑同一组包。Windows 上没有 gcc 时，可先用 winget 安装 MinGW-w64（`winget install BrechtSanders.WinLibs.POSIX.UCRT`，装完重开终端；`gcc --version` 应输出版本）。装好后也可以直接跑 `CGO_ENABLED=1 go test -race -count=1 -timeout 30m ./internal/api ./internal/lifecycle ./internal/verifier ./internal/store ./internal/decider ./internal/board ./internal/collab`。也可以改用 WSL/Docker。

harness 独立维护在 `kksty/cyber-harness` 的 `saker/fixes` 分支，构建方式见 [README](README.md)。gate 通过 `-harness-dir` / `HARNESS_DIR` 或默认的 `.build/harness-src` 找到源码，并按与 `scripts/build-harness.*` 相同的 tags 重建 Hub 与 scan；不要把 harness 源码或二进制复制进本仓库。需要宿主集成测试时，显式设置 `SAKER_HARNESS_BIN` 和 `SAKER_SCAN_BIN` 分别指向 Hub 与 scan 二进制；未设置时相关测试跳过，它们不替代 gate 的 harness 重建与清单记录。隔离验证及浏览器脚本见[验收说明](scripts/acceptance/README.md)。

AOP 生成代码是 vendored，不在 Saker 侧手改：`internal/aop/{pb,pty,traffic,tool,command,operation,guardrail}` 的 `*.pb.go` 是 cyber-harness 生成文件的副本，只允许把 import 改写到 `internal/aop` 路径（以及 `core/types` → `aop/command` 的 package 改名）。gate 的“AOP 生成代码漂移检查”步骤逐对比较 vendored 文件与 harness 源：比较前规范化 protoc 版本头、package 行、import 路径、本地注释与换行，再算 SHA-256；不一致时 gate 失败并打印首个差异与修复命令，harness 源缺失时该步骤记录 skip、不使 gate 失败（harness 构建步骤自己的缺失规则不变）。`-skip-harness` 只跳过 harness 二进制构建，只要源存在该检查仍会运行。构建清单的 `aop_proto` 字段记录状态、fingerprint、per-file 摘要与 harness commit；文件映射见 [internal/aop/pb/README.md](internal/aop/pb/README.md)。

升级 harness 后重新同步（只复制上游生成文件，不改语义；复制后立即运行同一漂移检查）：

Windows / PowerShell：

```powershell
pwsh -File scripts/sync-aop-protos.ps1
pwsh -File scripts/sync-aop-protos.ps1 -aop-dry-run   # 只列出将写入的差异，不写盘
```

Linux / macOS：

```bash
bash scripts/sync-aop-protos.sh
bash scripts/sync-aop-protos.sh -aop-dry-run
```

两个脚本与 gate 共用 `cmd/saker-gate` 的同一实现，harness 源解析顺序与 gate 相同（`-harness-dir`、`HARNESS_DIR`、`.build/harness-src`、同父目录 `cyber-harness`）。只跑检查而不写盘：`go run ./cmd/saker-gate -check-aop-protos`。同步后仍需按正常流程跑 gate 并提交更新后的生成文件。

## 变更要求

- 为行为修复添加覆盖真实失败模式的回归测试；协议、授权与数据模型改动需说明兼容性和失败模式。
- `internal/aop` 下的 `*.pb.go` 是 vendored 生成代码：不要手改，先改 harness fork 再运行同步脚本；任何 AOP 漂移检查失败都按修复指引重新同步，而不是放宽检查。
- 当前架构集中维护在 `docs/01-architecture.md`。修改行为时同步更新该文档或 README，不再新增阶段评审、路线图或交接文档。
- 一个 PR 聚焦一个问题，描述触发条件、前后行为与验证结果。
- 不提交 `data/`、`.build/`、`bin/`、`logs/`、`platform.yaml` 或数据快照。发布前用仓库的 `.gitleaks.toml` 扫描待发布内容。
- 不重启正在使用的控制面或宿主，不修改运行中的项目数据。验证使用临时目录与本地假模型。

## 反馈问题

Bug 请附版本或提交号、复现步骤、期望与实际行为及脱敏日志。功能建议请说明场景与边界。Saker 自身安全问题通过 [安全策略](SECURITY.md)规定的私密渠道报告，不在公开 issue 中披露利用细节。
