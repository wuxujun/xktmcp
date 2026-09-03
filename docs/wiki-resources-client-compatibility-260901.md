# Wiki Resources 客户端兼容性记录

> 测试日期：2026-09-02
> 服务端 SDK：github.com/modelcontextprotocol/go-sdk v1.7.0

| 客户端 / 传输 | 版本 | resources/list | templates/list | Catalog read | Page read | 结论 |
|:---|:---|:---:|:---:|:---:|:---:|:---:|
| Go SDK InMemory | v1.7.0 | PASS | PASS | PASS | PASS | PASS |
| Go SDK Streamable HTTP | v1.7.0 | PASS | PASS | PASS | PASS | PASS |
| Go SDK SSE | v1.7.0 | PASS | PASS | PASS | PASS | PASS |
| Claude Desktop | 未记录 | 未验证 | 未验证 | 未验证 | 未验证 | 未验证 |
| Cursor | 未记录 | 未验证 | 未验证 | 未验证 | 未验证 | 未验证 |
| VS Code | 1.136.0 | PASS | PASS | PASS | PASS | PASS |

## VS Code 实测说明

- 测试平台：macOS，VS Code 1.136.0。
- 传输方式：本地 stdio，使用 workspace `.vscode/mcp.json` 启动。
- Server 状态：Running；成功发现现有 5 个 Wiki Tools。
- `MCP: Browse Resources` 可发现并打开 `wiki://catalog`。
- Page Resource Template 可接受 `page_key` 并读取对应 Markdown 页面。
- Page 正文中的手机号由 `13812345678` 脱敏为 `138****5678`；Catalog 描述脱敏继续由 Go SDK 自动化测试覆盖。
- Claude Desktop 尚未配置本服务；Cursor 本机未安装，因此两行继续保持“未验证”。
