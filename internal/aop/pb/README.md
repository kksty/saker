# Vendored AOP 生成代码

本目录（以及 `pty/`、`traffic/`、`tool/`、`command/`、`operation/`、`guardrail/`）
下的 `*.pb.go` 是 cyber-harness 生成文件的副本。Saker不重新生成也不修改 AOP
wire 协议：只把 import 指到 `internal/aop` 并给 `command` 改 package 名，其余
字节保持上游生成结果。手改这些文件会让Saker的 wire 格式与 harness 静默分叉，
因此不要手工编辑；升级时用仓库根的同步脚本从 harness 源重新复制。

## 映射

| vendored（Saker） | 源（cyber-harness） | 复制时的改写 |
| --- | --- | --- |
| `internal/aop/pb/chat.pb.go` | `aop/chat.pb.go` | 无（仅版本头可能不同） |
| `internal/aop/pb/content.pb.go` | `aop/content.pb.go` | 无 |
| `internal/aop/pb/envelope.pb.go` | `aop/envelope.pb.go` | 无 |
| `internal/aop/pb/event.pb.go` | `aop/event.pb.go` | 无 |
| `internal/aop/pb/protocol.pb.go` | `aop/protocol.pb.go` | 无 |
| `internal/aop/pb/value.pb.go` | `aop/value.pb.go` | 无 |
| `internal/aop/operation/protocol.pb.go` | `aop/operation/protocol.pb.go` | 无 |
| `internal/aop/pty/protocol.pb.go` | `aop/pty/protocol.pb.go` | 无 |
| `internal/aop/tool/protocol.pb.go` | `aop/tool/protocol.pb.go` | import → `internal/aop/pb` |
| `internal/aop/traffic/protocol.pb.go` | `aop/traffic/protocol.pb.go` | import → `internal/aop/operation` |
| `internal/aop/command/command.pb.go` | `core/types/command.pb.go` | import → `internal/aop/pb`；package `types` → `command` |
| `internal/aop/guardrail/guardrail.pb.go` | `exts/guardrail/guardrail.pb.go` | import → `internal/aop/pb`、`internal/aop/operation` |

映射和改写规则只有一份实现：`internal/buildinfo/aopproto.go` 的
`AOPProtoFiles`。新增 vendored 生成文件时改那里，gate 检查与同步脚本会一起
生效。

## 检查

gate 的“AOP 生成代码漂移检查”步骤和下面两个模式使用同一实现
（`cmd/saker-gate`，`scripts/gate.*` 与 `scripts/sync-aop-protos.*` 只是薄入口）：

```text
go run ./cmd/saker-gate -check-aop-protos        # 只比较，不写盘
pwsh -File scripts/sync-aop-protos.ps1        # 从 harness 源复制，然后比较
bash scripts/sync-aop-protos.sh               # Linux/macOS 同上
```

比较不是逐字节 diff：先去掉注释与空行（含 `// Code generated` 下的
protoc-gen-go / protoc 版本行、proto 文档注释、Saker本地说明），把 package 行
按名字无关比较，把 `internal/aop` 的 import 折回 harness 路径，把 CRLF 归一为
LF，再做规范化 SHA-256。消息号、descriptor 字节与 enum 值都会参与比较，所以
协议语义的分叉一定失败；只有生成工具版本、本地注释、换行与上述路径改写不算
漂移。同步脚本写盘时统一用 LF。

harness 源缺失时检查输出 skip 原因并继续（不失败），构建清单仍记录 vendored
fingerprint；配置源的方式与 gate 相同：`-harness-dir`、`HARNESS_DIR`、
`.build/harness-src` 或同父目录的 `cyber-harness`。

## 升级流程

1. 更新 harness checkout（`tianwu/fixes`）并确认 `go build ./...` 通过。
2. 运行 `scripts/sync-aop-protos.ps1`（或 `.sh`）复制生成文件。
3. 查看 `git diff internal/aop`：应只有协议升级带来的生成代码变化；确认没有
   手工改动混入。
4. 运行完整 gate；构建清单的 `aop_proto.status` 应为 `ok`，`harness_commit`
   记录本次比较的 harness 提交。
5. 提交生成文件与因此产生的适配代码（不提交清单本身）。

升级步骤的权威说明在仓库根的 [贡献指南](../../../CONTRIBUTING.md)。
