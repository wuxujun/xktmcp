## xktmcp

基于MCP协议的学生相关数据接口服务。

### 运行

```bash
# 运行
go run ./cmd/server/main.go -transport=http -port=8081

# 查看日志
go run ./cmd/server/main.go -transport=http -port=8081 -debug
```

```bash
# 打包 (使用 -trimpath 移除编译时的绝对文件路径)
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -tags=jsoniter -ldflags="-s -w" -o mcp-server ./cmd/server/main.go
```

### Wiki 搜索后端

全部 Wiki 工具（`wiki_search`、`wiki_get_page`、`wiki_list_tree`、`wiki_upsert_page`、`wiki_get_backlinks`）支持远程 HTTP 和本地 llm-wiki Markdown 两种后端。复制示例配置：

```bash
cp config/wiki.example.json config/wiki.json
```

HTTP 模式继续调用 `BASE_URL/api/ai/wiki/search`：

```json
{
  "mode": "http"
}
```

本地模式搜索 llm-wiki 编译后的文章目录；`root` 相对路径以配置文件目录为基准：

```json
{
  "mode": "local",
  "local": {
    "root": "../.wiki",
    "content_dirs": ["wiki"],
    "write_dir": "wiki/topics",
    "default_category": "topics",
    "refresh_interval_seconds": 30,
    "max_file_size_bytes": 2097152
  }
}
```

默认读取 `config/wiki.json`，也可以通过 `-wiki-config=/path/to/wiki.json` 指定。配置文件不存在时默认使用 HTTP。Local 模式下，读取覆盖所有 `content_dirs`；新建、覆盖和追加只允许写入 `write_dir`（默认 `wiki/topics`），每次成功写入都会追加根目录 `log.md` 并立即刷新本地索引。反向链接同时识别相对 Markdown 链接和 `[[wiki link]]`。
