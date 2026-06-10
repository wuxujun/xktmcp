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