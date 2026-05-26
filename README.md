## xktmcp

基于MCP协议的学生相关数据接口服务。

### 运行

```bash
# 运行
go run ./cmd/server/main.go -transport=http -port=8080

# 查看日志
go run ./cmd/server/main.go -transport=http -port=8080 -debug
```

```bash
# 打包
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -tags=jsoniter -ldflags="-s -w" -o mcp-server ./cmd/server/main.go
```