# Wiki Resources Phase 1–2 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 为本地 Wiki 后端增加默认关闭、支持多租户隔离的 MCP Catalog、Tree 和 Page Resources 读取能力，不实施资源订阅。

**Architecture:** 在 `internal/wiki` 中实现页面 URI 编解码、脱敏 Catalog 和页面读取，并由 `LocalRouter` 按可信 userID 路由。`internal/server` 只静态注册不含租户数据的 `wiki://catalog`、`wiki://tree` 和 `wiki://page/{page_key}` 模板；共享 Server 不逐页注册资源。

**Tech Stack:** Go 1.25、`github.com/modelcontextprotocol/go-sdk v1.7.0`、标准库 `encoding/base64`/`encoding/json`、现有 `internal/pii`、Go `testing`、MCP InMemory/SSE/Streamable HTTP transports。

**Spec:** `docs/wiki-resources-capability-design-260901.md`

## Global Constraints

- 只实现设计的阶段 1–2；不配置 `SubscribeHandler`/`UnsubscribeHandler`，不调用 `ResourceUpdated`。
- Resources 只允许 `mode=local`，并由 `resources.enabled=true` 显式开启；默认必须保持关闭。
- 共享 Server 的 `resources/list` 只能包含 `wiki://catalog` 和 `wiki://tree`，不得包含页面标题、摘要、page ID 或 userID。
- 页面 URI 固定为 `wiki://page/{page_key}`；`page_key` 是 page ID 的无填充 Base64URL 编码，不提供 Raw 文件 URI。
- Resource Handler 从 `trace.EffectiveUserID(ctx, "")` 获取用户，不从 URI 或 JSON 内容读取 userID。
- 页面读取只能按当前用户索引中的 page ID 完成，不能把 URI 内容拼接为文件路径。
- Page、Catalog 和 Tree 响应都执行 `pii.Redact`/`pii.RedactJSON`。
- `max_catalog_entries` 默认 `1000`，合法范围 `1–10000`；Catalog 稳定排序后截断并返回 `total`/`truncated`。
- 保持 HTTP Wiki 模式和现有 11 个 Tools 的接口、注册与行为不变。
- 阶段 1–2 对任何 `subscriptions_enabled=true` 返回配置错误，禁止静默忽略未实现能力。
- `wiki://tree` 调用现有 `ListTree`，固定最大深度为 10。
- 每个行为变更严格执行 TDD：先看到目标测试因缺少行为而失败，再写最小实现。

## File Structure

| 文件 | 责任 |
|:---|:---|
| `internal/wiki/config.go` | 定义并校验 `ResourceConfig` |
| `internal/wiki/local_resources.go` | Page URI、Catalog 和脱敏页面读取 |
| `internal/wiki/local_router.go` | 按 userID 转发 Resource 请求 |
| `internal/server/wiki_resources.go` | MCP Resource 注册、Handler 和错误映射 |
| `cmd/server/wiki_resources_transport_test.go` | SSE/Streamable HTTP 冒烟测试 |
| 对应 `*_test.go` | 每个行为的 TDD 回归保护 |
| `docs/wiki-resources-client-compatibility-260901.md` | 记录已验证客户端/传输及明确未验证项 |
| `config/wiki.example.json`、`README.md`、进度报告 | 操作说明和完成状态 |

---

### Task 1: Resources Configuration Contract

**Files:**
- Modify: `internal/wiki/config.go`
- Modify: `internal/wiki/config_test.go`

**Interfaces:**
- Produces: `ResourceConfig`, `Config.Resources`, `DefaultMaxCatalogEntries`, `MaxCatalogEntriesLimit`
- Consumes: `LoadConfig`, `ModeHTTP`, `ModeLocal`, `LocalConfig.Users`

- [ ] **Step 1: Write failing configuration tests**

Add these literal cases to `internal/wiki/config_test.go`:

```go
func TestLoadConfigDefaultsResourcesDisabled(t *testing.T) {
	cfg, err := LoadConfig(filepath.Join(t.TempDir(), "missing.json"))
	if err != nil { t.Fatal(err) }
	if cfg.Resources.Enabled || cfg.Resources.SubscriptionsEnabled {
		t.Fatalf("resources defaults = %+v, want disabled", cfg.Resources)
	}
	if cfg.Resources.MaxCatalogEntries != 1000 {
		t.Fatalf("max_catalog_entries = %d, want 1000", cfg.Resources.MaxCatalogEntries)
	}
}

func TestLoadConfigNormalizesResources(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "wiki"), 0o700); err != nil { t.Fatal(err) }
	path := filepath.Join(dir, "wiki.json")
	raw := `{"mode":"local","resources":{"enabled":true},"local":{"root":"."}}`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil { t.Fatal(err) }
	cfg, err := LoadConfig(path)
	if err != nil { t.Fatal(err) }
	if !cfg.Resources.Enabled || cfg.Resources.MaxCatalogEntries != 1000 {
		t.Fatalf("resources = %+v", cfg.Resources)
	}
}
```

Add a table test using these literals; replace `ROOT` with a real temporary Wiki root before writing each config:

```go
tests := []struct { name, raw string }{
	{"http enabled", `{"mode":"http","resources":{"enabled":true}}`},
	{"subscriptions without resources", `{"mode":"local","resources":{"subscriptions_enabled":true},"local":{"root":"ROOT"}}`},
	{"subscriptions not implemented", `{"mode":"local","resources":{"enabled":true,"subscriptions_enabled":true},"local":{"root":"ROOT"}}`},
	{"negative limit", `{"mode":"local","resources":{"enabled":true,"max_catalog_entries":-1},"local":{"root":"ROOT"}}`},
	{"large limit", `{"mode":"local","resources":{"enabled":true,"max_catalog_entries":10001},"local":{"root":"ROOT"}}`},
	{"multi tenant subscriptions", `{"mode":"local","resources":{"enabled":true,"subscriptions_enabled":true},"local":{"root":"ROOT","users":{"u1":{"root":"ROOT"}}}}`},
}
```

- [ ] **Step 2: Run focused tests and verify RED**

Run: `go test ./internal/wiki -run 'TestLoadConfig(DefaultResourcesDisabled|NormalizesResources|RejectsInvalidResources)$' -count=1 -v`

Expected: build failure because `Config.Resources` does not exist.

- [ ] **Step 3: Add minimal configuration implementation**

Add to `internal/wiki/config.go`:

```go
const (
	DefaultMaxCatalogEntries = 1000
	MaxCatalogEntriesLimit   = 10000
)

type ResourceConfig struct {
	Enabled              bool `json:"enabled,omitempty"`
	SubscriptionsEnabled bool `json:"subscriptions_enabled,omitempty"`
	MaxCatalogEntries    int  `json:"max_catalog_entries,omitempty"`
}

type Config struct {
	Mode      string         `json:"mode"`
	Resources ResourceConfig `json:"resources,omitempty"`
	Local     LocalConfig    `json:"local"`
}

func normalizeResourceConfig(cfg *Config) error {
	if cfg.Resources.MaxCatalogEntries == 0 { cfg.Resources.MaxCatalogEntries = DefaultMaxCatalogEntries }
	if cfg.Resources.MaxCatalogEntries < 1 || cfg.Resources.MaxCatalogEntries > MaxCatalogEntriesLimit {
		return fmt.Errorf("wiki resources.max_catalog_entries must be between 1 and %d", MaxCatalogEntriesLimit)
	}
	if cfg.Resources.SubscriptionsEnabled { return errors.New("wiki resource subscriptions are not supported yet") }
	if cfg.Resources.Enabled && cfg.Mode != ModeLocal { return errors.New("wiki resources.enabled requires local mode") }
	return nil
}
```

Initialize missing-file configuration with `MaxCatalogEntries: DefaultMaxCatalogEntries`. Normalize decoded zero to 1000, reject values outside 1–10000, require local mode for enabled Resources, and return `wiki resource subscriptions are not supported yet` whenever `SubscriptionsEnabled` is true. For local mode validate Resources after `normalizeLocalConfig`; for HTTP validate before return. The future phase 3 will replace the temporary blanket rejection with single-tenant validation.

- [ ] **Step 4: Verify GREEN**

Run:

```bash
gofmt -w internal/wiki/config.go internal/wiki/config_test.go
go test ./internal/wiki -run TestLoadConfig -count=1 -v
go test ./internal/wiki -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/wiki/config.go internal/wiki/config_test.go
git commit -m "feat: configure wiki resources"
```

---

### Task 2: Page URI, Catalog, and Redacted Page Reads

**Files:**
- Create: `internal/wiki/local_resources.go`
- Create: `internal/wiki/local_resources_test.go`

**Interfaces:**
- Consumes: `LocalSearcher.refresh`, `LocalSearcher.GetPage`, `localDocument`, `pii.Redact`
- Produces: `ResourceDescriptor`, `ResourceCatalog`, `PageResourceURI`, `ParsePageResourceURI`, `LocalSearcher.ListResources`, `LocalSearcher.ReadPageResource`, `ErrResourceNotFound`

- [ ] **Step 1: Write failing canonical URI tests**

Create `internal/wiki/local_resources_test.go`:

```go
package wiki

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPageResourceURIRoundTrip(t *testing.T) {
	const pageID = "wiki/topics/student-guide"
	const wantURI = "wiki://page/d2lraS90b3BpY3Mvc3R1ZGVudC1ndWlkZQ"
	uri, err := PageResourceURI(pageID)
	if err != nil || uri != wantURI { t.Fatalf("uri=%q err=%v", uri, err) }
	got, err := ParsePageResourceURI(uri)
	if err != nil || got != pageID { t.Fatalf("pageID=%q err=%v", got, err) }
}

func TestParsePageResourceURIRejectsNonCanonicalInput(t *testing.T) {
	for _, uri := range []string{"", "wiki://page/", "wiki://raw/file.md", "wiki://page/%%%", "wiki://page/YQ==", "wiki://page/YQ?x=1", "wiki://page/YQ#x", "wiki://page/YQ/extra", "WIKI://page/YQ"} {
		t.Run(uri, func(t *testing.T) {
			if _, err := ParsePageResourceURI(uri); !errors.Is(err, ErrResourceNotFound) {
				t.Fatalf("error=%v, want ErrResourceNotFound", err)
			}
		})
	}
}
```

- [ ] **Step 2: Run URI tests and verify RED**

Run: `go test ./internal/wiki -run 'Test(PageResourceURIRoundTrip|ParsePageResourceURIRejectsNonCanonicalInput)$' -count=1 -v`

Expected: build failure because the URI API does not exist.

- [ ] **Step 3: Implement the URI codec**

Create `internal/wiki/local_resources.go` with these exact rules:

```go
const (
	pageResourcePrefix    = "wiki://page/"
	maxResourcePageIDBytes = 2048
)

var ErrResourceNotFound = errors.New("wiki resource not found")

func PageResourceURI(pageID string) (string, error) {
	if pageID == "" || strings.TrimSpace(pageID) != pageID || len(pageID) > maxResourcePageIDBytes || !utf8.ValidString(pageID) {
		return "", ErrResourceNotFound
	}
	return pageResourcePrefix + base64.RawURLEncoding.EncodeToString([]byte(pageID)), nil
}

func ParsePageResourceURI(raw string) (string, error) {
	if !strings.HasPrefix(raw, pageResourcePrefix) { return "", ErrResourceNotFound }
	key := strings.TrimPrefix(raw, pageResourcePrefix)
	if key == "" || strings.ContainsAny(key, "/?#%=") { return "", ErrResourceNotFound }
	decoded, err := base64.RawURLEncoding.DecodeString(key)
	if err != nil || len(decoded) == 0 || len(decoded) > maxResourcePageIDBytes || !utf8.Valid(decoded) { return "", ErrResourceNotFound }
	pageID := string(decoded)
	canonical, err := PageResourceURI(pageID)
	if err != nil || canonical != raw { return "", ErrResourceNotFound }
	return pageID, nil
}
```

- [ ] **Step 4: Verify URI tests GREEN**

Run `gofmt` on both files, then rerun the Step 2 command. Expected: PASS.

- [ ] **Step 5: Add failing Catalog and page-read tests**

Add `TestLocalSearcherListResourcesSortsRedactsAndTruncates`. Create `a.md` and `z.md` with explicit page IDs; put phone `13812345678` in Z title and ID card `11010519491231002X` in its summary. Call `ListResources(ctx, 1)` and assert literal values:

```go
if catalog.Total != 2 || !catalog.Truncated || len(catalog.Items) != 1 { t.Fatalf("catalog=%+v", catalog) }
if catalog.Items[0].Name != "A Page" || catalog.Items[0].URI != "wiki://page/YS1wYWdl" { t.Fatalf("item=%+v", catalog.Items[0]) }
```

Call again with limit 2 and assert Z title equals `Z 138****5678` and its description does not contain the raw ID card.

Add `TestLocalSearcherReadPageResourceRedactsContent`: create page ID `private-page` with body `Call 13812345678.`, compute its URI, and assert returned content equals `Call 138****5678.`. A URI for page ID `missing` must return `ErrResourceNotFound`.

Add `TestLocalSearcherListResourcesRejectsDuplicatePageID`: create two indexed files with the same explicit page ID and assert `errors.Is(err, ErrDuplicateResourcePageID)`.

```go
catalog, err := searcher.ListResources(context.Background(), 2)
if !errors.Is(err, ErrDuplicateResourcePageID) || catalog.Total != 0 {
	t.Fatalf("catalog=%+v err=%v", catalog, err)
}
```

- [ ] **Step 6: Run Catalog/page tests and verify RED**

Run: `go test ./internal/wiki -run 'TestLocalSearcher(ListResources|ReadPageResource)' -count=1 -v`

Expected: build failure because Resource types and LocalSearcher methods do not exist.

- [ ] **Step 7: Implement Catalog and page reads**

Add JSON types and the duplicate sentinel:

```go
var ErrDuplicateResourcePageID = errors.New("duplicate wiki resource page_id")

type ResourceDescriptor struct {
	URI         string `json:"uri"`
	Name        string `json:"name"`
	Description string `json:"description"`
	MIMEType    string `json:"mimeType"`
}
type ResourceCatalog struct {
	Total     int                  `json:"total"`
	Truncated bool                 `json:"truncated"`
	Items     []ResourceDescriptor `json:"items"`
}
```

`ListResources` must call `refresh(ctx, false)`, reject limit < 1, copy document metadata while holding one read lock, check `ctx.Err()` per item, reject duplicate page IDs, and release the lock before sorting. Keep `{pageID, descriptor}` pairs and sort by page ID. Build each item with:

```go
description := fmt.Sprintf("分类: %s | 摘要: %s", doc.result.Category, doc.result.Summary)
ResourceDescriptor{URI: uri, Name: pii.Redact(doc.result.Title), Description: pii.Redact(description), MIMEType: "text/markdown"}
```

Set `Total` before truncation, set `Truncated`, and return a non-nil empty Items slice for an empty Wiki. Implement page reads only through the index:

```go
func (s *LocalSearcher) ReadPageResource(ctx context.Context, uri string) (string, error) {
	pageID, err := ParsePageResourceURI(uri)
	if err != nil { return "", err }
	page, err := s.GetPage(ctx, "", pageID, "")
	if errors.Is(err, errLocalPageNotFound) { return "", ErrResourceNotFound }
	if err != nil { return "", err }
	return pii.Redact(page.Content), nil
}
```

- [ ] **Step 8: Verify all Wiki package tests**

Run:

```bash
gofmt -w internal/wiki/local_resources.go internal/wiki/local_resources_test.go
go test ./internal/wiki -run 'Test(PageResource|ParsePageResource|LocalSearcherListResources|LocalSearcherReadPageResource)' -count=1 -v
go test ./internal/wiki -count=1
```

Expected: PASS.

- [ ] **Step 9: Commit**

```bash
git add internal/wiki/local_resources.go internal/wiki/local_resources_test.go
git commit -m "feat: add local wiki resource catalog"
```

---

### Task 3: Multi-Tenant Resource Routing

**Files:**
- Modify: `internal/wiki/local_router.go`
- Modify: `internal/wiki/local_router_test.go`

**Interfaces:**
- Consumes: `LocalSearcher.ListResources`, `LocalSearcher.ReadPageResource`, `LocalRouter.searcher`
- Produces: `LocalRouter.ListResources(ctx, userID, limit)`, `LocalRouter.ReadPageResource(ctx, userID, uri)`

- [ ] **Step 1: Write failing isolation tests**

Add a helper that creates `wiki/article.md` with explicit `page_id: shared-page`, a supplied title, and supplied body. Create user A and B roots using the same page ID but different content, then assert:

```go
catalogA, err := router.ListResources(context.Background(), "user-a", 10)
if err != nil || len(catalogA.Items) != 1 || catalogA.Items[0].Name != "甲手册" { t.Fatalf("catalog=%+v err=%v", catalogA, err) }
uri := catalogA.Items[0].URI
pageA, err := router.ReadPageResource(context.Background(), "user-a", uri)
if err != nil || pageA != "甲内容 138****5678" { t.Fatalf("pageA=%q err=%v", pageA, err) }
pageB, err := router.ReadPageResource(context.Background(), "user-b", uri)
if err != nil || pageB != "乙内容 139****5678" { t.Fatalf("pageB=%q err=%v", pageB, err) }
if _, err := router.ListResources(context.Background(), "unknown", 10); !errors.Is(err, ErrUserWikiNotConfigured) { t.Fatalf("error=%v", err) }
```

Add `TestLocalRouterResourcesFallBackToDefault` with `RequireUserMapping=false`; both new methods called with an unknown user must return the default Catalog/page.

- [ ] **Step 2: Run router tests and verify RED**

Run: `go test ./internal/wiki -run 'TestLocalRouter(IsolatesResources|ResourcesFallBackToDefault)$' -count=1 -v`

Expected: build failure because the router methods do not exist.

- [ ] **Step 3: Add minimal forwarding methods**

```go
func (r *LocalRouter) ListResources(ctx context.Context, userID string, limit int) (ResourceCatalog, error) {
	searcher, err := r.searcher(userID)
	if err != nil { return ResourceCatalog{}, err }
	return searcher.ListResources(ctx, limit)
}

func (r *LocalRouter) ReadPageResource(ctx context.Context, userID, uri string) (string, error) {
	searcher, err := r.searcher(userID)
	if err != nil { return "", err }
	return searcher.ReadPageResource(ctx, uri)
}
```

- [ ] **Step 4: Verify GREEN and race safety**

Run:

```bash
gofmt -w internal/wiki/local_router.go internal/wiki/local_router_test.go
go test ./internal/wiki -run TestLocalRouter -count=1 -v
go test -race ./internal/wiki -count=1
```

Expected: PASS without race reports.

- [ ] **Step 5: Commit**

```bash
git add internal/wiki/local_router.go internal/wiki/local_router_test.go
git commit -m "feat: isolate wiki resources by user"
```

---

### Task 4: MCP Resource Registration and Handlers

**Files:**
- Create: `internal/server/wiki_resources.go`
- Create: `internal/server/wiki_resources_test.go`
- Modify: `internal/server/register.go`
- Modify: `internal/server/register_test.go`

**Interfaces:**
- Consumes: `wiki.ResourceConfig`, router Resource methods, `LocalRouter.ListTree`, `trace.EffectiveUserID`, `pii.RedactJSON`
- Produces: `registerWikiResources`, Catalog/Tree/Page `mcp.ResourceHandler` constructors

- [ ] **Step 1: Write failing opt-in registration tests**

Factor the existing in-memory connect sequence in `register_test.go` into `connectRegisteredServer(t, configPath)`. Add a local config with one `guide` page and `resources.enabled:true`, connect a client, sort the listed URIs, and assert:

```go
if len(uris) != 2 || uris[0] != "wiki://catalog" || uris[1] != "wiki://tree" { t.Fatalf("uris=%v", uris) }
templates, err := clientSession.ListResourceTemplates(ctx, nil)
if err != nil { t.Fatal(err) }
if len(templates.ResourceTemplates) != 1 || templates.ResourceTemplates[0].URITemplate != "wiki://page/{page_key}" { t.Fatalf("templates=%+v", templates.ResourceTemplates) }
catalogRead, err := clientSession.ReadResource(ctx, &mcp.ReadResourceParams{URI: "wiki://catalog"})
if err != nil || len(catalogRead.Contents) != 1 { t.Fatalf("catalog=%+v err=%v", catalogRead, err) }
var catalog wikibackend.ResourceCatalog
if err := json.Unmarshal([]byte(catalogRead.Contents[0].Text), &catalog); err != nil || len(catalog.Items) != 1 { t.Fatalf("catalog=%+v err=%v", catalog, err) }
pageRead, err := clientSession.ReadResource(ctx, &mcp.ReadResourceParams{URI: catalog.Items[0].URI})
if err != nil || len(pageRead.Contents) != 1 || pageRead.Contents[0].Text != "Body" { t.Fatalf("page=%+v err=%v", pageRead, err) }
```

Use a literal length/element comparison after `sort.Strings`; do not add a comparison dependency. Extend the default-disabled local registration test: its five Wiki Tools remain unchanged, and both `ListResources` and `ListResourceTemplates` must return zero entries.

- [ ] **Step 2: Run registration tests and verify RED**

Run: `go test ./internal/server -run 'TestRegisterAllLocalWiki' -count=1 -v`

Expected: opt-in test FAIL because no Resources are registered.

- [ ] **Step 3: Write failing direct Handler tests**

Create a real multi-user `LocalRouter` in `wiki_resources_test.go`. Call handler constructors directly with `trace.WithAuthenticatedUserID` contexts and `mcp.ReadResourceRequest{Params:&mcp.ReadResourceParams{URI:...}}`. Assert:

- user A Catalog JSON contains only A’s title and redacted phone;
- user A Tree JSON contains no B title;
- the same shared Page URI resolves to A content under ctxA and B content under ctxB;
- invalid Page URI and unknown strict-mapping user return `mcp.CodeResourceNotFound`;
- canceled context returns `context.Canceled`;
- an unexpected fake-backend error containing `/private/root` becomes literal `read wiki resource failed` without exposing the path.

Use this concrete result/error shape in the tests:

```go
result, err := pageHandler(ctxA, &mcp.ReadResourceRequest{Params: &mcp.ReadResourceParams{URI: pageURI}})
if err != nil || len(result.Contents) != 1 || result.Contents[0].Text != "甲内容 138****5678" {
	t.Fatalf("result=%+v err=%v", result, err)
}
_, err = pageHandler(ctxA, &mcp.ReadResourceRequest{Params: &mcp.ReadResourceParams{URI: "wiki://page/%%%"}})
var rpcErr *jsonrpc.Error
if !errors.As(err, &rpcErr) || rpcErr.Code != mcp.CodeResourceNotFound {
	t.Fatalf("error=%v, want resource not found", err)
}
```

Define a `failingWikiResourceBackend` implementing all three interface methods and returning `errors.New("read /private/root failed")`; assert the returned error string is exactly `read wiki resource failed`.

- [ ] **Step 4: Implement a narrow backend interface and handlers**

Create `internal/server/wiki_resources.go`:

```go
type wikiResourceBackend interface {
	ListResources(context.Context, string, int) (wikibackend.ResourceCatalog, error)
	ReadPageResource(context.Context, string, string) (string, error)
	ListTree(context.Context, string, string, int) ([]model.WikiNode, error)
}
```

Each handler resolves `userID := trace.EffectiveUserID(ctx, "")`. Catalog JSON-marshals the already-redacted catalog; Tree calls `ListTree(ctx, userID, "", 10)` and uses `pii.RedactJSON(nodes)`; Page uses the already-redacted Markdown. Reject nil Params and mismatched fixed URIs as Resource Not Found. Return exactly one content item:

```go
&mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: req.Params.URI, MIMEType: mimeType, Text: text}}}
```

Implement safe mapping:

```go
func wikiResourceError(ctx context.Context, uri string, err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) { return err }
	if errors.Is(err, wikibackend.ErrResourceNotFound) || errors.Is(err, wikibackend.ErrUserWikiNotConfigured) { return mcp.ResourceNotFoundError(uri) }
	logger.ErrorfCtx(ctx, "read wiki resource failed: uri=%s error=%v", pii.MaskSubject(uri), err)
	return errors.New("read wiki resource failed")
}
```

Register only tenant-neutral static metadata:

```go
func registerWikiResources(s *mcp.Server, backend wikiResourceBackend, cfg wikibackend.ResourceConfig) {
	s.AddResource(&mcp.Resource{URI: "wiki://catalog", Name: "wiki-catalog", Title: "Wiki 资源目录", MIMEType: "application/json"}, wikiCatalogHandler(backend, cfg.MaxCatalogEntries))
	s.AddResource(&mcp.Resource{URI: "wiki://tree", Name: "wiki-tree", Title: "Wiki 目录树", MIMEType: "application/json"}, wikiTreeHandler(backend))
	s.AddResourceTemplate(&mcp.ResourceTemplate{URITemplate: "wiki://page/{page_key}", Name: "wiki-page", Title: "Wiki 页面", MIMEType: "text/markdown"}, wikiPageHandler(backend))
}
```

In `registerWikiTools`, retain the concrete `*LocalRouter` and call `registerWikiResources` only for local mode with `wikiConfig.Resources.Enabled`. Do not add Resources to `MCP_ENABLED_TOOLS`.

- [ ] **Step 5: Verify handlers and registration GREEN**

Run:

```bash
gofmt -w internal/server/wiki_resources.go internal/server/wiki_resources_test.go internal/server/register.go internal/server/register_test.go
go test ./internal/server -run 'Test(WikiResource|RegisterAllLocalWiki)' -count=1 -v
go test ./internal/server -count=1
go test ./internal/wiki ./internal/server ./internal/tools -count=1
```

Expected: PASS; existing Tools tests remain unchanged.

- [ ] **Step 6: Commit**

```bash
git add internal/server/wiki_resources.go internal/server/wiki_resources_test.go internal/server/register.go internal/server/register_test.go
git commit -m "feat: register local wiki resources"
```

---

### Task 5: Transport Compatibility, Documentation, and Final Verification

**Files:**
- Create: `cmd/server/wiki_resources_transport_test.go`
- Create: `docs/wiki-resources-client-compatibility-260901.md`
- Modify: `config/wiki.example.json`
- Modify: `README.md`
- Modify: `docs/wiki-progress-report-260901.md`

**Interfaces:**
- Consumes: `server.RegisterAll`, `newStreamableHTTPHandler`, `mcp.NewSSEHandler`, `mcp.Transport`
- Produces: transport evidence and operator documentation; no new production API

- [ ] **Step 1: Add Streamable HTTP and SSE smoke tests**

Create a helper that writes one page titled `Transport Guide` plus a local config with Resources enabled. Add this shared assertion:

```go
func assertWikiResourcesOverTransport(t *testing.T, transport mcp.Transport) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client := mcp.NewClient(&mcp.Implementation{Name: "resource-test", Version: "1.0.0"}, nil)
	session, err := client.Connect(ctx, transport, nil)
	if err != nil { t.Fatal(err) }
	defer session.Close()
	listed, err := session.ListResources(ctx, nil)
	if err != nil || len(listed.Resources) != 2 { t.Fatalf("resources=%+v err=%v", listed, err) }
	templates, err := session.ListResourceTemplates(ctx, nil)
	if err != nil || len(templates.ResourceTemplates) != 1 { t.Fatalf("templates=%+v err=%v", templates, err) }
	read, err := session.ReadResource(ctx, &mcp.ReadResourceParams{URI: "wiki://catalog"})
	if err != nil || len(read.Contents) != 1 || !strings.Contains(read.Contents[0].Text, `"name":"Transport Guide"`) {
		t.Fatalf("catalog=%+v err=%v", read, err)
	}
	var catalog wikibackend.ResourceCatalog
	if err := json.Unmarshal([]byte(read.Contents[0].Text), &catalog); err != nil || len(catalog.Items) != 1 { t.Fatalf("decoded catalog=%+v err=%v", catalog, err) }
	page, err := session.ReadResource(ctx, &mcp.ReadResourceParams{URI: catalog.Items[0].URI})
	if err != nil || len(page.Contents) != 1 || page.Contents[0].Text != "Transport body" { t.Fatalf("page=%+v err=%v", page, err) }
}
```

Build a fresh MCP Server and call `mcp_server.RegisterAll` in each subtest. Use exact transports:

```go
t.Run("streamable_http", func(t *testing.T) {
	server := newWikiResourceTestServer(t, configPath)
	httpServer := httptest.NewServer(newStreamableHTTPHandler(server))
	defer httpServer.Close()
	assertWikiResourcesOverTransport(t, &mcp.StreamableClientTransport{Endpoint: httpServer.URL})
})
t.Run("sse", func(t *testing.T) {
	server := newWikiResourceTestServer(t, configPath)
	handler := mcp.NewSSEHandler(func(*http.Request) *mcp.Server { return server }, nil)
	httpServer := httptest.NewServer(handler)
	defer httpServer.Close()
	assertWikiResourcesOverTransport(t, &mcp.SSEClientTransport{Endpoint: httpServer.URL})
})
```

- [ ] **Step 2: Run transport tests**

Run: `go test ./cmd/server -run TestWikiResourcesTransports -count=1 -v`

Expected: both `streamable_http` and `sse` PASS. A transport failure must be diagnosed and fixed in registration/handler behavior; do not skip or delete the case.

- [ ] **Step 3: Update configuration and bilingual documentation**

Add this default-safe block to `config/wiki.example.json`:

```json
"resources": {
  "enabled": false,
  "subscriptions_enabled": false,
  "max_catalog_entries": 1000
}
```

In `README.md`, document in Chinese and English:

- Resources are local-mode-only and default disabled;
- enabling registers Catalog, Tree, and Page Template;
- shared multi-tenant servers expose page metadata only through the caller-specific Catalog;
- subscriptions are not implemented in phases 1–2 and must remain disabled.

After the InMemory and transport tests have passed, create `docs/wiki-resources-client-compatibility-260901.md` with this exact table. Product rows remain `未验证` until a named product version is manually tested:

```markdown
# Wiki Resources 客户端兼容性记录

> 测试日期：2026-09-01
> 服务端 SDK：github.com/modelcontextprotocol/go-sdk v1.7.0

| 客户端 / 传输 | 版本 | resources/list | templates/list | Catalog read | Page read | 结论 |
|:---|:---|:---:|:---:|:---:|:---:|:---:|
| Go SDK InMemory | v1.7.0 | PASS | PASS | PASS | PASS | PASS |
| Go SDK Streamable HTTP | v1.7.0 | PASS | PASS | PASS | PASS | PASS |
| Go SDK SSE | v1.7.0 | PASS | PASS | PASS | PASS | PASS |
| Claude Desktop | 未记录 | 未验证 | 未验证 | 未验证 | 未验证 | 未验证 |
| Cursor | 未记录 | 未验证 | 未验证 | 未验证 | 未验证 | 未验证 |
| VS Code | 未记录 | 未验证 | 未验证 | 未验证 | 未验证 | 未验证 |
```

Only after Step 4 passes, update `docs/wiki-progress-report-260901.md`: mark Resources phases 1–2 complete and retain subscriptions as optional/not implemented.

- [ ] **Step 4: Run fresh final verification**

Run every command and read complete output:

```bash
gofmt -w internal/wiki/config.go internal/wiki/config_test.go internal/wiki/local_resources.go internal/wiki/local_resources_test.go internal/wiki/local_router.go internal/wiki/local_router_test.go internal/server/wiki_resources.go internal/server/wiki_resources_test.go internal/server/register.go internal/server/register_test.go cmd/server/wiki_resources_transport_test.go
go test ./... -count=1
go test -race ./internal/wiki ./internal/server ./internal/tools -count=1
go vet ./...
git diff --check
```

Expected: all exit 0; no skipped transport tests, race reports, vet findings, or whitespace errors.

- [ ] **Step 5: Review line-by-line against the spec**

Confirm with code/test evidence:

- `resources/list` has only Catalog and Tree;
- `resources/templates/list` has only one Page template;
- static metadata contains no tenant page data;
- Catalog and Page use the effective user’s LocalSearcher;
- invalid/cross-tenant reads return Resource Not Found without path disclosure;
- PII is redacted in Catalog, Tree, and Page;
- HTTP Wiki and default-disabled configs register no Resources;
- no subscription handler or notification code was added.

- [ ] **Step 6: Commit transport tests and docs**

```bash
git add cmd/server/wiki_resources_transport_test.go config/wiki.example.json README.md docs/wiki-progress-report-260901.md docs/wiki-resources-client-compatibility-260901.md
git commit -m "test: verify wiki resources transports"
```

- [ ] **Step 7: Confirm clean branch state**

Run:

```bash
git status --short
git log -5 --oneline
```

Expected: empty status and scoped commits for configuration, local resources, tenant routing, MCP registration, and transport/docs verification.
