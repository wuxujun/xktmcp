package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/joho/godotenv"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/wuxujun/xktmcp/internal/auth"
	"github.com/wuxujun/xktmcp/internal/logger"
	mcp_server "github.com/wuxujun/xktmcp/internal/server"
	"gopkg.in/natefinch/lumberjack.v2"
)

func main() {
	_ = godotenv.Load()
	// 命令行参数(认证令牌不再有硬编码默认值,改由 env AUTH_TOKEN 提供,避免默认弱口令)。
	transport := flag.String("transport", "stdio", "传输方式: stdio, sse 或 http")
	port := flag.Int("port", 8080, "HTTP/SSE 模式下的监听端口")
	logFilePath := flag.String("logfile", "server.log", "日志文件路径")
	authTokenFlag := flag.String("auth-token", "", "Bearer 本地令牌;留空则回退读取环境变量 AUTH_TOKEN")
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

	// 构建认证器(仅用于 http/sse 网络传输;stdio 为本地传输,免认证)。
	// 本地令牌来源:命令行 -auth-token 优先,否则环境变量 AUTH_TOKEN。
	localToken := *authTokenFlag
	if localToken == "" {
		localToken = strings.TrimSpace(os.Getenv("AUTH_TOKEN"))
	}
	authenticator := auth.New(buildAuthConfig(localToken))

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

		requireAuth(authenticator, "sse")
		finalHandler := authenticator.Middleware(sseHandler)

		mux := http.NewServeMux()
		// 健康检查端点(免认证,供探针使用)
		mux.HandleFunc("/health", healthHandler)
		// 客户端连接 /sse 路径来建立事件流
		mux.Handle("/sse", finalHandler)
		// 客户端通过 POST /messages/... 发送 JSON-RPC 消息
		mux.Handle("/messages/", finalHandler)

		addr := fmt.Sprintf(":%d", *port)
		logger.Infof("正在通过 SSE 启动 xkt-student-server，监听地址 %s/sse...", addr)
		runServer(addr, mux)

	case "http":
		// 创建 Streamable HTTP 处理器
		handler := mcp.NewStreamableHTTPHandler(func(request *http.Request) *mcp.Server {
			return s
		}, nil)

		requireAuth(authenticator, "http")
		finalHandler := authenticator.Middleware(handler)

		mux := http.NewServeMux()
		// 健康检查端点(免认证,供探针使用)
		mux.HandleFunc("/health", healthHandler)
		// Streamable HTTP 默认通过单一路径处理
		mux.Handle("/mcp", finalHandler)

		addr := fmt.Sprintf(":%d", *port)
		logger.Infof("正在通过 Streamable HTTP 启动 xkt-mcp-server，监听地址 %s/mcp...", addr)
		runServer(addr, mux)

	default:
		logger.Errorf("未知的传输方式: %s (请使用 stdio, sse 或 http)", *transport)
		os.Exit(1)
	}
}

// buildAuthConfig 从环境变量装配认证配置。
//
// 远程兜底验证默认【关闭】,仅当显式设置 AUTH_REMOTE_VERIFY_URL 时启用,
// 且其主机必须出现在 AUTH_REMOTE_ALLOWED_HOSTS 白名单中(防 SSRF)。
func buildAuthConfig(localToken string) auth.Config {
	var allowed []string
	if raw := strings.TrimSpace(os.Getenv("AUTH_REMOTE_ALLOWED_HOSTS")); raw != "" {
		for _, h := range strings.Split(raw, ",") {
			if h = strings.TrimSpace(h); h != "" {
				allowed = append(allowed, h)
			}
		}
	}
	return auth.Config{
		LocalToken:      localToken,
		RemoteVerifyURL: strings.TrimSpace(os.Getenv("AUTH_REMOTE_VERIFY_URL")),
		AllowedHosts:    allowed,
	}
}

// requireAuth 对网络传输(http/sse)执行 fail-closed:未配置任何认证方式则拒绝启动。
// stdio 为本地传输,不调用此函数(免认证)。
func requireAuth(a *auth.Authenticator, transport string) {
	if !a.Enabled() {
		logger.Errorf("[Auth] %s 传输要求认证,但未配置 AUTH_TOKEN(或 -auth-token)/AUTH_REMOTE_VERIFY_URL,拒绝启动", transport)
		os.Exit(1)
	}
}

// healthHandler 是免认证的存活探针,返回 200 与简单 JSON。
func healthHandler(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}

// runServer 启动 HTTP 服务并支持优雅关闭。
//
// 超时策略:仅设 ReadHeaderTimeout(防 Slowloris 慢速请求头攻击)与 IdleTimeout;
// 【刻意不设】ReadTimeout/WriteTimeout,因为 SSE 与 Streamable HTTP 都是长连接流式传输,
// 设了会中途掐断正常的流。
//
// 优雅关闭:监听 SIGINT/SIGTERM,收到后用带超时的 ctx 调用 srv.Shutdown,
// 让在途请求自然结束,再退出。
func runServer(addr string, handler http.Handler) {
	srv := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	shutdownDone := make(chan struct{})
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		<-sigCh
		logger.Infof("收到关闭信号,正在优雅关闭 (最长等待 15s)...")
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			logger.Errorf("优雅关闭超时/出错: %v", err)
		}
		close(shutdownDone)
	}()

	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		logger.Errorf("HTTP 服务错误: %v", err)
		os.Exit(1)
	}
	<-shutdownDone
	logger.Infof("服务已优雅关闭")
}
