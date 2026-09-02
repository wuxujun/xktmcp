# MCP Resources 能力接入 LLM-Wiki 设计方案

> 文件归档：`docs/wiki-resources-capability-design-260901.md`
> 创建日期：2026-09-01
> 修订日期：2026-09-01
> 状态：待评审
> 适用范围：`mode=local` 的 LLM-Wiki
> SDK 基线：`github.com/modelcontextprotocol/go-sdk v1.7.0`

---

## 1. 结论与定位

MCP Resources 用于把本地 Wiki 的目录和 Markdown 页面作为只读上下文提供给 MCP 客户端，与现有 Wiki Tools 互补：

- Tools 继续承担搜索、读取、写入和反向引用等模型自主操作；
- Resources 提供目录浏览、确定性 URI 读取和可选的资源更新通知；
- Resources 不是 Wiki 核心功能的前置条件，仅在目标客户端实际消费 MCP Resources 时启用；
- 第一阶段只支持本地 Markdown 后端，HTTP Wiki 后端不注册 Resources。

该能力实施复杂度为中等。页面读取可以复用现有索引，但多租户资源发现不能直接注册所有页面：Go SDK v1.7.0 的 `resources/list` 来自 Server 级静态注册表，无法按请求用户动态过滤。

---

## 2. 设计目标与非目标

### 2.1 目标

1. 提供当前用户 Wiki 目录、资源目录和页面正文的标准 MCP Resource 读取能力。
2. 复用 `LocalRouter` 的用户映射，确保读取结果按认证主体隔离。
3. 不向客户端暴露本地绝对路径、相对文件路径或其他租户的资源元数据。
4. 页面正文、资源目录描述和目录树输出统一执行响应级 PII 脱敏。
5. 保持所有写入经 `wiki_upsert_page` 完成，不为 Resources 增加写操作。
6. 为后续订阅与更新通知保留稳定、可计算的资源 URI。

### 2.2 非目标

1. 第一阶段不支持 HTTP Wiki 后端 Resources。
2. 不提供 `wiki://raw/{rel_path}`，避免重复能力和额外路径攻击面。
3. 不承诺所有 MCP 客户端都支持 `@` 选择或资源订阅；上线前按目标客户端和版本建立兼容性矩阵。
4. 共享多租户 Server 不在 `resources/list` 中逐页枚举 Wiki 页面。
5. 不修改 Markdown 文件格式，不生成额外索引文件。

---

## 3. 当前约束

### 3.1 SDK 资源列表是服务级静态集合

Go SDK v1.7.0 通过以下实例方法注册资源：

```go
server.AddResource(resource, handler)
server.AddResourceTemplate(template, handler)
server.RemoveResources(uri...)
server.ResourceUpdated(ctx, &mcp.ResourceUpdatedNotificationParams{URI: uri})
```

`resources/list` 直接分页返回 Server 的静态资源注册表，不调用用户自定义 List Handler。因此，如果把所有租户页面都注册为独立 Resource，即使 `resources/read` 能阻止越权，列表仍会泄露其他租户的 URI、标题和摘要。

本设计只在静态列表中注册不含租户数据的 `wiki://catalog` 与 `wiki://tree`，页面通过 Resource Template 读取。

### 3.2 Resource 请求没有显式 userID 参数

`resources/read` 只有 URI。处理器必须从请求上下文获取可信主体：

```go
userID := trace.EffectiveUserID(ctx, "")
```

- HTTP/SSE：优先使用认证中间件写入的认证主体；
- 带 `userId` 路由参数但没有认证主体时，沿用现有请求上下文路由；
- stdio：没有网络认证主体，只能使用默认 Wiki；启用 `require_user_mapping` 时不支持空主体读取。

### 3.3 订阅需要显式启用

仅调用 `Server.ResourceUpdated` 不会自动开放订阅端点。必须在创建 `mcp.Server` 时同时配置 `SubscribeHandler` 与 `UnsubscribeHandler`。当前启动流程先创建 Server、后在 `RegisterAll` 中创建 LocalRouter，因此订阅属于独立的第三阶段启动装配改造。

SDK 的订阅表按 URI 关联会话，`ResourceUpdated` 会通知所有订阅相同 URI 的会话，不会再按调用者 userID 过滤。如果两个租户存在相同 `page_id`，它们会产生相同 Page URI。因此，共享多租户 Server 必须禁用订阅；订阅只允许没有 `local.users` 映射的单租户部署，或者每租户独立 Server。

### 3.4 配置开关

在 Wiki 顶层配置增加独立 Resources 配置，默认关闭，保持现有部署行为不变：

```json
{
  "mode": "local",
  "resources": {
    "enabled": true,
    "subscriptions_enabled": false,
    "max_catalog_entries": 1000
  }
}
```

- `enabled`：是否注册 Catalog、Tree 和 Page Template，默认 `false`；
- `subscriptions_enabled`：是否启用第三阶段订阅，默认 `false`，且只能在 `enabled=true` 的单租户/独立 Server 中开启；
- `max_catalog_entries`：Catalog 最大返回条目数，默认 `1000`，合法范围 `1–10000`；
- `mode=http` 时设置 `enabled=true` 直接返回配置错误，避免出现配置已开启但能力未生效的静默状态。
- `local.users` 非空时设置 `subscriptions_enabled=true` 返回配置错误，阻止按 URI 广播造成跨租户误通知。

---

## 4. Resource URI 设计

| URI | MIME 类型 | 用途 | `resources/list` 可见 |
|:---|:---:|:---|:---:|
| `wiki://catalog` | `application/json` | 当前用户可见页面的脱敏资源目录 | 是 |
| `wiki://tree` | `application/json` | 当前用户 Wiki 完整目录树 | 是 |
| `wiki://page/{page_key}` | `text/markdown` | 指定页面的 Markdown 正文 | 通过模板 |

### 4.1 `page_key`

`page_key` 是 `page_id` 的无填充 Base64URL 编码：

```text
page_id = wiki/topics/student-guide
page_key = d2lraS90b3BpY3Mvc3R1ZGVudC1ndWlkZQ
URI      = wiki://page/d2lraS90b3BpY3Mvc3R1ZGVudC1ndWlkZQ
```

采用不透明编码的原因：

- `page_id` 可能包含 `/`，不适合作为单一 URI 路径段直接插入；
- URI 路径段不携带 `/`、`..` 等路径语法；Base64URL 仅用于稳定编码，不作为保密手段；
- 解码后按索引中的 `page_id` 查找，不把客户端输入拼接成本地路径；
- Catalog 可以直接返回完整 URI，客户端无需自行编码。

解码后必须校验：非空、编码规范、长度受限，并且页面确实存在于当前用户索引；任何失败统一返回 Resource Not Found，避免泄露租户或路径信息。

---

## 5. 总体架构

```mermaid
graph TD
    Client[MCP Client] -->|resources/list| Static[静态资源注册表]
    Static --> CatalogURI[wiki://catalog]
    Static --> TreeURI[wiki://tree]
    Client -->|resources/templates/list| PageTemplate[wiki://page/{page_key}]
    Client -->|resources/read| Handler[Wiki Resource Handler]
    Handler --> Identity[认证主体 / 路由 userID]
    Identity --> Router[LocalRouter]
    Router --> Searcher[用户 LocalSearcher]
    Searcher --> Snapshot[内存索引快照]
    Snapshot --> Redact[PII 脱敏]
    Redact --> Client
    Upsert[wiki_upsert_page] --> Events[可选 ResourceEvents]
    Events --> Notify[ResourceUpdated]
```

### 5.1 Wiki 资源领域层

在 `internal/wiki/local_resources.go` 增加：

```go
type ResourceDescriptor struct {
    URI         string
    Name        string
    Description string
    MIMEType    string
}

func (s *LocalSearcher) ListResources(ctx context.Context) ([]ResourceDescriptor, error)
func (s *LocalSearcher) ReadResource(ctx context.Context, uri string) (mimeType, text string, err error)
```

行为要求：

- `ListResources` 从一次加读锁的索引快照生成稳定排序的目录；
- Catalog 中的 URI 使用页面真实 `page_id` 计算 `page_key`；
- 页面读取通过索引定位，不直接接受或读取客户端提供的文件路径；
- Tree 复用现有目录树逻辑；
- 所有循环检查 `ctx.Err()`；
- 返回前对正文、标题、摘要和描述执行 `pii.Redact`。

### 5.2 用户路由层

在 `LocalRouter` 增加同名转发方法。每次调用先通过现有 `searcher(userID)` 选择当前用户实例，严格映射失败时向协议层返回统一不可见错误。

资源 URI 不包含 userID。租户选择完全来自可信请求上下文，避免用户通过修改 URI 切换租户。

### 5.3 MCP 注册层

仅在 `wikiConfig.Mode == local` 时注册：

```go
s.AddResource(&mcp.Resource{
    URI:      "wiki://catalog",
    Name:     "wiki-catalog",
    Title:    "Wiki 资源目录",
    MIMEType: "application/json",
}, catalogHandler)

s.AddResource(&mcp.Resource{
    URI:      "wiki://tree",
    Name:     "wiki-tree",
    Title:    "Wiki 目录树",
    MIMEType: "application/json",
}, treeHandler)

s.AddResourceTemplate(&mcp.ResourceTemplate{
    URITemplate: "wiki://page/{page_key}",
    Name:        "wiki-page",
    Title:       "Wiki 页面",
    MIMEType:    "text/markdown",
}, pageHandler)
```

固定资源的名称与描述不得包含租户内容。实际 Catalog、Tree 和 Page 内容在 `resources/read` 时按当前用户生成。

### 5.4 错误映射

| 内部错误 | MCP 表现 |
|:---|:---|
| URI 不支持、`page_key` 非法、页面不存在 | `mcp.ResourceNotFoundError(uri)` |
| 用户未配置、跨租户页面不可见 | `mcp.ResourceNotFoundError(uri)` |
| context 取消或超时 | 原样返回 context 错误 |
| 索引刷新或序列化失败 | 通用读取失败，不返回本地路径 |

日志记录原始内部错误供排查，但审计日志中的 URI、page ID 和用户标识继续使用现有掩码策略。

---

## 6. 多租户资源发现策略

### 6.1 本期方案：Catalog Resource

共享 MCP Server 的静态列表只暴露两个通用入口。读取 `wiki://catalog` 时返回当前用户页面目录：

```json
{
  "total": 1,
  "truncated": false,
  "items": [
    {
      "uri": "wiki://page/d2lraS90b3BpY3Mvc3R1ZGVudC1ndWlkZQ",
      "name": "学员服务指南",
      "description": "分类: topics | 摘要: 学员日常排课与请假规范说明",
      "mimeType": "text/markdown"
    }
  ]
}
```

Catalog 先按页面 ID 稳定排序，再应用 `max_catalog_entries`。超过上限时返回前 N 项，同时设置真实 `total` 和 `truncated=true`；调用方仍可使用 `wiki_search` 定位未列出的页面。

优点：不泄露其他租户元数据，不依赖 SDK 定制，HTTP、SSE 和 stdio 使用相同读取模型。限制是部分客户端的资源选择器只展示 `wiki://catalog`，不会把 Catalog 内的页面展开成独立选择项。

### 6.2 未来方案：每租户独立 Server

如果产品必须在客户端选择器中逐页展示资源，应为每个租户创建独立 MCP Server/端点，并在各自 Server 上静态注册该租户页面。不要在共享 Server 上注册所有租户页面，也不为此维护 SDK Fork。

---

## 7. 订阅与更新通知（第三阶段）

### 7.1 启动装配

第三阶段仅面向单租户或每租户独立 Server。先把 Wiki 本地运行时的创建从 `RegisterAll` 中拆出，使 `main` 在 `mcp.NewServer` 前获得资源订阅校验器，再同时配置：

```go
&mcp.ServerOptions{
    KeepAlive:          30 * time.Second,
    SubscribeHandler:   resourceSubscriptions.Subscribe,
    UnsubscribeHandler: resourceSubscriptions.Unsubscribe,
}
```

订阅处理器必须：

- 只接受 `wiki://catalog`、`wiki://tree` 和合法的 `wiki://page/{page_key}`；
- 从 context 解析当前用户，并验证页面对该用户可见；
- 对无权限和不存在统一返回 Not Found；
- 不自行维护 SDK 已管理的 session 订阅表。

共享多租户 Server 不配置这两个 Handler，也不声明 Resource Subscribe capability；Catalog、Tree 和 Page 的读取能力不受影响。

### 7.2 写入事件解耦

工具层不直接依赖 `mcp.Server`。为 Wiki Upsert Handler 注入可选事件接口：

```go
type WikiResourceEvents interface {
    PageChanged(ctx context.Context, userID, pageID, status string)
}
```

Upsert 成功、索引刷新成功且缓存失效后触发事件。Server 适配器根据 `pageID` 计算 URI，并统一通知：

- 对应 `wiki://page/{page_key}`；
- `wiki://catalog`；
- `wiki://tree`。

统一发送最多产生少量冗余通知，但不需要工具层判断标题、分类或目录是否实际变化，规则更稳定。

通知使用：

```go
s.ResourceUpdated(ctx, &mcp.ResourceUpdatedNotificationParams{URI: uri})
```

由于静态资源列表本身没有变化，不需要也不存在公开的 `NotifyResourceListChanged` 调用。

### 7.3 外部文件变化

当前索引按需周期刷新。第三阶段在刷新前后比较页面 ID、版本和更新时间快照：

- 页面新增、删除或重命名：通知 Catalog 和 Tree；
- 页面内容或版本变化：通知对应 Page 和 Catalog；
- 没有变化：不发送通知。

通知失败只记录日志，不回滚已经成功的 Wiki 写入。

---

## 8. 安全设计

1. **身份来源**：Resource Handler 只信任认证主体或现有请求路由上下文，不从 URI 解析 userID。
2. **列表隔离**：共享 Server 不注册租户页面元数据；Catalog 在读取时按用户生成。
3. **订阅隔离**：共享多租户 Server 禁用按 URI 广播的资源订阅，避免跨租户误通知和更新时间侧信道。
4. **路径安全**：删除 Raw URI；页面只按当前用户索引中的 `page_id` 查找，不把 URI 内容拼接成本地路径。
5. **符号链接防护**：继续沿用索引刷新时忽略符号链接的策略。
6. **PII 脱敏**：正文、标题、摘要、描述和 Tree JSON 全部经过 `pii.Redact`。
7. **容量限制**：继续使用 `max_file_size_bytes`；Catalog 按 `max_catalog_entries` 截断，并显式返回 `total` 与 `truncated`。
8. **只读边界**：所有 Resource Handler 无文件写入能力。
9. **错误最小化**：客户端错误不携带本地根目录、文件路径、租户列表或目标页面是否存在于其他租户的信息。

---

## 9. 文件改造清单

| 文件 | 改造内容 | 阶段 |
|:---|:---|:---:|
| `internal/wiki/local_resources.go` | URI 编解码、Catalog/Tree/Page 读取 | 1 |
| `internal/wiki/local_resources_test.go` | 领域层与安全边界测试 | 1 |
| `internal/wiki/local_router.go` | 按用户转发 Resources | 1 |
| `internal/wiki/local_router_test.go` | 多租户资源隔离测试 | 1 |
| `internal/server/wiki_resources.go` | MCP Resource 注册及 Handler | 2 |
| `internal/server/wiki_resources_test.go` | MCP 内存传输集成测试 | 2 |
| `internal/server/register.go` | 仅本地模式注册 Resources | 2 |
| `internal/wiki/config.go` | Resources 开关、Catalog 上限及配置校验 | 2 |
| `internal/wiki/config_test.go` | 默认关闭、范围约束与模式冲突测试 | 2 |
| `cmd/server/main.go` | 第三阶段订阅启动装配 | 3 |
| `internal/tools/wiki.go` | 注入可选资源事件，不直接依赖 Server | 3 |
| `config/wiki.example.json` | 说明 Resources 仅适用于 local 模式 | 2 |

---

## 10. 测试与验收

### 10.1 必测场景

#### URI 与读取

- `page_id` 与 `page_key` 往返一致；
- 非法 Base64URL、空值、超长值和未知页面返回 Not Found；
- URI 查询串、片段、额外路径段和不支持的 scheme/host 被拒绝；
- Catalog 排序稳定，URI 可被 Page Handler 读取；
- Catalog 超过配置上限时稳定截断并返回正确 `total/truncated`；
- Page、Catalog 和 Tree 内容均完成 PII 脱敏。

#### 多租户

- 用户 A 的 Catalog 不包含用户 B 的标题、URI、摘要；
- 用户 A 使用用户 B 的 `page_key` 读取时返回 Not Found；
- `require_user_mapping=true` 时，未知或空用户不能读取默认 Wiki；
- stdio 默认 Wiki 行为有明确回归测试。

#### MCP 协议

- `resources/list` 只返回 Catalog 与 Tree；
- `resources/templates/list` 返回 Page 模板；
- `resources/read` 的 URI、MIME 类型与脱敏内容正确；
- HTTP、SSE 与内存传输至少各完成一次兼容性验证。

#### 订阅（第三阶段）

- Subscribe/Unsubscribe 成对启用；
- 共享多租户配置拒绝启用订阅；
- 无权限页面无法订阅；
- Upsert 失败不发送通知；
- Upsert 成功发送 Page、Catalog 和 Tree 通知；
- 外部文件变化只在快照实际变化时通知；
- 通知失败不改变 Upsert 成功结果。

### 10.2 验收标准

1. `go test ./...`、`go test -race ./internal/wiki ./internal/server ./internal/tools` 和 `go vet ./...` 全部通过。
2. 共享多租户 Server 的静态 Resource 元数据不包含任何租户页面信息。
3. 不存在通过 URI、错误消息或资源描述获取其他租户信息的路径。
4. Resources 未启用时，或 HTTP Wiki 模式保持默认 Resources 关闭时，现有 11 个 Tools 行为不变。
5. 目标 MCP 客户端的实际 Resources 支持情况形成版本化联调记录，不再使用“所有主流客户端均支持”的无版本结论。

---

## 11. 实施顺序

| 阶段 | 内容 | 预计工作量 | 是否阻塞下一阶段 |
|:---:|:---|:---:|:---:|
| 1 | URI、Catalog/Tree/Page 领域能力和多租户测试 | 1 天 | 是 |
| 2 | MCP 注册、协议集成测试、客户端基础联调 | 1 天 | 否 |
| 3 | 单租户订阅装配、Upsert 事件、外部变化通知 | 1–2 天 | 否，可独立评估必要性 |

建议先完成阶段 1–2。只有目标客户端确认消费资源订阅时，再实施阶段 3，避免为暂未使用的通知能力增加启动装配复杂度。

---

## 12. 最终决策摘要

- Resources 是可选增强，不替代 Wiki Tools；
- 仅为本地 Wiki 后端提供 Resources；
- 共享 Server 使用 `wiki://catalog`、`wiki://tree` 和 `wiki://page/{page_key}` 模板；
- 不提供 Raw URI，不在共享 Server 逐页注册多租户资源；
- Page 读取基于索引中的 page ID，不基于客户端文件路径；
- 第一、二阶段完成安全读取，第三阶段按客户端需求实施订阅通知；
- 共享多租户 Server 禁用订阅；需要订阅时采用单租户或每租户独立 Server；
- 若必须在选择器中逐页展示资源，采用每租户独立 MCP Server，而不是共享列表或 SDK Fork。
