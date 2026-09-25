# 发布构建验证记录

记录创建：2026-09-24 · 复测：2026-09-25 · 基线提交：`b37d09a` · 状态：本地预检通过，远端 CI 待运行

本记录针对[下一阶段方案](next-phase-design-2026-09-24.md)的 P2。当前工作树仍有未提交改动，因此基线提交号不是本次变更的最终提交号；发布前必须以最终提交的 CI 结果为准。

## 构建约定与本次变更

`AGENTS.md`、README 和既有发布审查均使用 Linux amd64、`CGO_ENABLED=0`、`-trimpath` 与 `-tags=jsoniter` 作为发布构建口径。本次在现有 `.github/workflows/ci.yml` 的 `build-release` 步骤补齐 `-tags=jsoniter`，保留版本号注入、静态构建与产物上传。构建后新增 stdio 冒烟步骤：以临时文件目录和纯 `file_*` 白名单启动生成的二进制，完成 MCP 初始化并核对 `tools/list` 的三个文件工具。冒烟测试不需要上游令牌或真实业务数据。

## 本地验证结果

| 检查 | 结果 |
| --- | --- |
| 原生平台使用 `CGO_ENABLED=0 go build -trimpath -tags=jsoniter -ldflags='-s -w -X main.version=smoke'` 构建单文件入口 | 通过；产物用于实际 MCP 冒烟测试。 |
| `MCP_RELEASE_BINARY=<原生产物> go test ./cmd/server -run '^TestReleaseBinarySmoke$' -count=1 -timeout=30s` | 通过；`initialize` 与 `tools/list` 成功，工具集合恰为 `file_get_info`、`file_read_preview`、`file_search`。 |
| `go test ./cmd/server -count=1` | 通过；未设置 `MCP_RELEASE_BINARY` 时冒烟测试按设计跳过。 |
| `go test -p=1 ./... -count=1 -timeout=120s` | 通过；所有含测试的包均通过。 |
| `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -tags=jsoniter -ldflags='-s -w -X main.version=smoke'` 构建单文件入口 | 通过；`file` 确认为静态 Linux amd64 ELF。 |
| `go version -m` 检查 Linux 产物 | 确认 Go 1.25.13、`-tags=jsoniter`、`-trimpath=true`、`CGO_ENABLED=0`、`GOOS=linux`、`GOARCH=amd64`。 |

本机为 macOS amd64，无法直接运行 Linux ELF；Linux 产物的协议冒烟将由 Ubuntu CI 的 `build-release` 作业执行。本地冒烟在沙箱中因无法读取 Go 默认构建缓存而被拒绝，获准在沙箱外复测后通过；这不是测试断言失败。冒烟子进程使用独立的最小环境变量集合，不继承测试进程的其他凭据。一次未设置包并行上限的全仓测试因耗时异常被主动中止；随后按既有发布门禁使用 `-p=1` 重新运行并通过。

## 远端发布门禁

- [ ] 变更形成最终提交后，记录提交号和同一提交的 CI run 链接。
- [ ] 确认现有测试、竞态检查、静态分析、漏洞扫描和 lint 作业通过。
- [ ] 确认 `build-release` 的 Linux amd64 构建、产物检查及新增 MCP 冒烟步骤通过。
- [ ] 下载该 CI run 的产物，按[部署指南](deployment-guide.md)在隔离环境核对工具白名单、认证与探针；目标环境验收另行记录。

远端 CI 尚未运行在本次工作树改动上，不能将本记录视为正式发布批准。若远端产物或测试失败，先保留失败日志和提交号，再按对应失败项修复并重新执行同一发布门禁。
