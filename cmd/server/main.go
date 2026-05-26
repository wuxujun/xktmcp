package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/joho/godotenv"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/wuxujun/xktmcp/internal/logger"
	mcp_server "github.com/wuxujun/xktmcp/internal/server"
	"gopkg.in/natefinch/lumberjack.v2"
)

func main() {
	_ = godotenv.Load()
	// 定义命令行参数
	transport := flag.String("transport", "stdio", "传输方式: stdio, sse 或 http")
	port := flag.Int("port", 8080, "HTTP/SSE 模式下的监听端口")
	logFilePath := flag.String("logfile", "server.log", "日志文件路径")
	authToken := flag.String("auth-token", "XKT#2026", "启用 Bearer Token 认证（若为空则不启用）")
	flag.Parse()

	// 配置日志自动分割 (Lumberjack)
	logWriter := &lumberjack.Logger{
		Filename:   *logFilePath,
		MaxSize:    100,  // 每个日志文件最大 100MB
		MaxBackups: 7,    // 保留最近 7 个备份
		MaxAge:     7,    // 保留最近 7 天的日志
		Compress:   true, // 压缩旧日志
	}

	// 初始化全局日志
	logger.Init(io.MultiWriter(os.Stderr, logWriter))

	s := mcp.NewServer(&mcp.Implementation{
		Name:    "xkt-student-server",
		Version: "1.0.0",
	}, &mcp.ServerOptions{
		// 启用心跳功能，每 30 秒发送一次 ping
		KeepAlive: 30 * time.Second,
	})

	if err := mcp_server.RegisterAll(s); err != nil {
		logger.Errorf("无法注册工具: %v", err)
		os.Exit(1)
	}

	// 创建认证中间件
	// 针对 SSE 的特殊性，我们支持从 Header 或 URL 参数 (?token=xxx) 中获取 Token
	authWrapper := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// 1. 尝试从 Header 获取
			token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			// 2. 尝试从 Query 参数获取
			if token == "" {
				token = r.URL.Query().Get("token")
			}

			// 验证逻辑
			if *authToken != "" && token == *authToken {
				logger.Infof("[Auth] 验证通过: %s %s", r.Method, r.URL.Path)
				next.ServeHTTP(w, r)
				return
			}

			// 如果本地没配或不匹配，尝试远程验证 (可选)
			if token != "" {
				baseURL := strings.TrimRight(os.Getenv("STUDENT_BASE_URL"), "/")
				if baseURL == "" {
					baseURL = "http://localhost:8080"
				}
				verifyURL := fmt.Sprintf("%s/api/auth/check", baseURL)
				verifyReq, _ := http.NewRequestWithContext(r.Context(), "GET", verifyURL, nil)
				verifyReq.Header.Set("Authorization", "Bearer "+token)
				client := &http.Client{Timeout: 3 * time.Second}
				resp, err := client.Do(verifyReq)
				if err == nil && resp.StatusCode == http.StatusOK {
					logger.Infof("[Auth] 远程验证通过: %s %s", r.Method, r.URL.Path)
					next.ServeHTTP(w, r)
					return
				}
			}

			logger.Errorf("[Auth] 验证失败: %s %s, token=%s", r.Method, r.URL.Path, token)
			w.Header().Set("WWW-Authenticate", `Bearer realm="mcp"`)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
		})
	}

	switch *transport {
	case "stdio":
		logger.Infof("正在通过 stdio 启动 xkt-student-server...")
		// 启动 stdio 传输
		if err := s.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
			logger.Errorf("Stdio 运行错误: %v", err)
			os.Exit(1)
		}

	case "sse":
		// 创建 SSE 处理器
		sseHandler := mcp.NewSSEHandler(func(request *http.Request) *mcp.Server {
			return s
		}, nil)

		var finalHandler http.Handler = sseHandler
		if authWrapper != nil {
			finalHandler = authWrapper(sseHandler)
		}

		mux := http.NewServeMux()
		// 客户端连接 /sse 路径来建立事件流
		mux.Handle("/sse", finalHandler)
		// 客户端通过 POST /messages/... 发送 JSON-RPC 消息
		mux.Handle("/messages/", finalHandler)

		addr := fmt.Sprintf(":%d", *port)
		logger.Infof("正在通过 SSE 启动 xkt-student-server，监听地址 %s/sse...", addr)
		if err := http.ListenAndServe(addr, mux); err != nil {
			logger.Errorf("HTTP 服务错误: %v", err)
			os.Exit(1)
		}

	case "http":
		// 创建 Streamable HTTP 处理器
		handler := mcp.NewStreamableHTTPHandler(func(request *http.Request) *mcp.Server {
			return s
		}, nil)

		var finalHandler http.Handler = handler
		if authWrapper != nil {
			finalHandler = authWrapper(handler)
		}

		mux := http.NewServeMux()
		// Streamable HTTP 默认通过单一路径处理
		mux.Handle("/mcp", finalHandler)

		addr := fmt.Sprintf(":%d", *port)
		logger.Infof("正在通过 Streamable HTTP 启动 xkt-student-server，监听地址 %s/mcp...", addr)
		if err := http.ListenAndServe(addr, mux); err != nil {
			logger.Errorf("HTTP 服务错误: %v", err)
			os.Exit(1)
		}

	default:
		logger.Errorf("未知的传输方式: %s (请使用 stdio, sse 或 http)", *transport)
		os.Exit(1)
	}
}
