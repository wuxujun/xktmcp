package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/joho/godotenv"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/wuxujun/xktmcp/internal/auth"
	"github.com/wuxujun/xktmcp/internal/logger"
	"github.com/wuxujun/xktmcp/internal/metrics"
	mcp_server "github.com/wuxujun/xktmcp/internal/server"
	"github.com/wuxujun/xktmcp/internal/trace"
	"gopkg.in/natefinch/lumberjack.v2"
)

func main() {
	_ = godotenv.Load()
	// 命令行参数(认证令牌不再有硬编码默认值,改由 env AUTH_TOKEN 提供,避免默认弱口令)。
	transport := flag.String("transport", "stdio", "传输方式: stdio, sse 或 http")
	port := flag.Int("port", 8080, "HTTP/SSE 模式下的监听端口")
	logFilePath := flag.String("logfile", "server.log", "日志文件路径")
	logHTTPPayloads := flag.Bool("log-http-payloads", envBool("LOG_HTTP_PAYLOADS"), "是否记录 HTTP 请求 Body 与响应结果")
	logHTTPPayloadMaxBytes := flag.Int64("log-http-payload-max-bytes", envInt64("LOG_HTTP_PAYLOAD_MAX_BYTES", 1024*1024), "单个 HTTP 请求/响应最多记录字节数；0 表示不截断")
	authTokenFlag := flag.String("auth-token", "", "Bearer 本地令牌;留空则回退读取环境变量 AUTH_TOKEN")
	wikiConfigPath := flag.String("wiki-config", "config/wiki.json", "Wiki 搜索后端配置文件路径")
	flag.Parse()

	// 配置日志自动分割 (Lumberjack)
	logWriter := &lumberjack.Logger{
		Filename:   *logFilePath,
		MaxSize:    100,  // 每个日志文件最大 100MB
		MaxBackups: 7,    // 保留最近 7 个备份
		MaxAge:     7,    // 保留最近 7 天的日志
		Compress:   true, // 压缩旧日志
		LocalTime:  true, // 使用本地时间命名备份文件
	}

	// 初始化全局日志
	logger.Init(io.MultiWriter(os.Stderr, logWriter))
	if *logHTTPPayloadMaxBytes < 0 {
		logger.Errorf("日志配置非法: log-http-payload-max-bytes 不能小于 0")
		os.Exit(1)
	}
	payloadLogConfig := httpPayloadLogConfig{Enabled: *logHTTPPayloads, MaxBytes: *logHTTPPayloadMaxBytes}
	logger.Infof("HTTP 请求/响应内容日志 enabled=%t max_bytes=%d", payloadLogConfig.Enabled, payloadLogConfig.MaxBytes)

	// 启动协程：每天凌晨自动切分日志 (按天记录)
	go func() {
		for {
			now := time.Now()
			// 计算到明天凌晨 0 点的等待时间
			next := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, now.Location())
			timer := time.NewTimer(next.Sub(now))
			<-timer.C

			logger.Infof("开始执行每日日志自动轮转...")
			if err := logWriter.Rotate(); err != nil {
				logger.Errorf("每日日志轮转失败: %v", err)
			}
		}
	}()

	s := mcp.NewServer(&mcp.Implementation{
		Name:    "xkt-mcp-server",
		Version: "1.0.1",
	}, &mcp.ServerOptions{
		// 启用心跳功能，每 30 秒发送一次 ping
		KeepAlive: 30 * time.Second,
	})

	if err := mcp_server.RegisterAll(s, *wikiConfigPath); err != nil {
		logger.Errorf("无法注册工具: %v", err)
		os.Exit(1)
	}

	// 构建认证器(仅用于 http/sse 网络传输;stdio 为本地传输,免认证)。
	// 本地令牌来源:命令行 -auth-token 优先,否则环境变量 AUTH_TOKEN。
	localToken := *authTokenFlag
	if localToken == "" {
		localToken = strings.TrimSpace(os.Getenv("AUTH_TOKEN"))
	}
	authenticator, err := auth.New(buildAuthConfig(localToken))
	if err != nil {
		logger.Errorf("认证配置非法: %v", err)
		os.Exit(1)
	}

	// 将 MCPMiddleware 注册到 MCP Server：
	// MCP SDK 会 detach HTTP request context，所以在 HTTP 中间件注入的 userID
	// 在 tool handler 里取不到。MCPMiddleware 在 MCP 消息层重新补注，
	// 使 trace.EffectiveUserID(ctx, args.UserID) 能透明取到远程验证返回的 userID。
	s.AddReceivingMiddleware(authenticator.MCPMiddleware())

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
		// Prometheus 指标端点(免认证,供抓取;如需保护可置于网络隔离或反代后)
		mux.Handle("/metrics", metrics.Handler())
		// 客户端连接 /sse 路径来建立事件流
		mux.Handle("/sse", userIDMiddleware(finalHandler))
		// 客户端通过 POST /messages/... 发送 JSON-RPC 消息
		mux.Handle("/messages/", userIDMiddleware(finalHandler))

		addr := fmt.Sprintf(":%d", *port)
		logger.Infof("正在通过 SSE 启动 xkt-student-server，监听地址 %s/sse...", addr)
		runServer(addr, requestLoggingMiddleware(mux, payloadLogConfig))

	case "http":
		// 创建 Streamable HTTP 处理器
		handler := newStreamableHTTPHandler(s)

		requireAuth(authenticator, "http")
		finalHandler := authenticator.Middleware(handler)

		mux := http.NewServeMux()
		// 健康检查端点(免认证,供探针使用)
		mux.HandleFunc("/health", healthHandler)
		// Prometheus 指标端点(免认证,供抓取;如需保护可置于网络隔离或反代后)
		mux.Handle("/metrics", metrics.Handler())
		// Streamable HTTP 默认通过单一路径处理
		mux.Handle("/mcp", userIDMiddleware(finalHandler))

		addr := fmt.Sprintf(":%d", *port)
		logger.Infof("正在通过 Streamable HTTP 启动 xkt-mcp-server，监听地址 %s/mcp...", addr)
		runServer(addr, requestLoggingMiddleware(mux, payloadLogConfig))

	default:
		logger.Errorf("未知的传输方式: %s (请使用 stdio, sse 或 http)", *transport)
		os.Exit(1)
	}
}

const protocolDetectionMaxBytes = 4 << 20

// newStreamableHTTPHandler 按协议版本选择传输语义：legacy 协议使用有状态会话并
// 返回 application/json，2026-07-28 使用无会话模式并保持 text/event-stream。
func newStreamableHTTPHandler(server *mcp.Server) http.Handler {
	getServer := func(*http.Request) *mcp.Server {
		return server
	}
	legacyJSONHandler := mcp.NewStreamableHTTPHandler(getServer, &mcp.StreamableHTTPOptions{
		JSONResponse: true,
	})
	modernSSEHandler := mcp.NewStreamableHTTPHandler(getServer, &mcp.StreamableHTTPOptions{
		Stateless: true,
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		protocolVersion := requestProtocolVersion(r)
		if isLegacyProtocolVersion(protocolVersion) {
			legacyJSONHandler.ServeHTTP(w, r)
			return
		}
		modernSSEHandler.ServeHTTP(w, r)
	})
}

func requestProtocolVersion(r *http.Request) string {
	if version := strings.TrimSpace(r.Header.Get("Mcp-Protocol-Version")); version != "" {
		return version
	}
	if r.Method != http.MethodPost || r.Body == nil || r.Body == http.NoBody {
		return ""
	}
	originalBody := r.Body
	body, err := io.ReadAll(io.LimitReader(originalBody, protocolDetectionMaxBytes+1))
	r.Body = &replayReadCloser{
		Reader: io.MultiReader(bytes.NewReader(body), originalBody),
		Closer: originalBody,
	}
	if err != nil || len(body) > protocolDetectionMaxBytes {
		return ""
	}
	var envelope struct {
		Params struct {
			ProtocolVersion string                     `json:"protocolVersion"`
			Meta            map[string]json.RawMessage `json:"_meta"`
		} `json:"params"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return ""
	}
	if version := strings.TrimSpace(envelope.Params.ProtocolVersion); version != "" {
		return version
	}
	var version string
	_ = json.Unmarshal(envelope.Params.Meta["io.modelcontextprotocol/protocolVersion"], &version)
	return strings.TrimSpace(version)
}

func isLegacyProtocolVersion(version string) bool {
	switch version {
	case "2025-11-25", "2025-06-18", "2025-03-26", "2024-11-05":
		return true
	default:
		return false
	}
}

// buildAuthConfig 从环境变量装配认证配置。
//
// 远程兜底验证默认【关闭】,仅当显式设置 AUTH_REMOTE_VERIFY_URL 时启用,
// 且其主机必须出现在 AUTH_REMOTE_ALLOWED_HOSTS 白名单中(防 SSRF)。
//
// IP 白名单(AUTH_IP_ALLOWLIST,逗号分隔 CIDR)默认【关闭】;配置后,
// 命中网段的请求直接放行、无需 Bearer 令牌。来源 IP 默认取 TCP 连接的 RemoteAddr,
// 仅当 AUTH_TRUST_FORWARDED_HEADER=true(部署在可信代理之后)时才信任 X-Forwarded-For。
func buildAuthConfig(localToken string) auth.Config {
	var allowed []string
	if raw := strings.TrimSpace(os.Getenv("AUTH_REMOTE_ALLOWED_HOSTS")); raw != "" {
		for _, h := range strings.Split(raw, ",") {
			if h = strings.TrimSpace(h); h != "" {
				allowed = append(allowed, h)
			}
		}
	}

	// 解析受信任来源网段(IP 白名单);非法 CIDR 直接 fail-closed 拒绝启动。
	var cidrs []*net.IPNet
	if raw := strings.TrimSpace(os.Getenv("AUTH_IP_ALLOWLIST")); raw != "" {
		parsed, err := auth.ParseCIDRs(strings.Split(raw, ","))
		if err != nil {
			logger.Errorf("[Auth] AUTH_IP_ALLOWLIST 配置非法,拒绝启动: %v", err)
			os.Exit(1)
		}
		cidrs = parsed
	}

	// 解析多租户配置
	var tenants []auth.TenantConfig
	if raw := strings.TrimSpace(os.Getenv("AUTH_TENANTS")); raw != "" {
		if err := json.Unmarshal([]byte(raw), &tenants); err != nil {
			logger.Errorf("[Auth] 解析 AUTH_TENANTS 环境变量失败: %v", err)
			os.Exit(1)
		}
	}

	return auth.Config{
		LocalToken:           localToken,
		Tenants:              tenants,
		RemoteVerifyURL:      strings.TrimSpace(os.Getenv("AUTH_REMOTE_VERIFY_URL")),
		AllowedHosts:         allowed,
		AllowedCIDRs:         cidrs,
		TrustForwardedHeader: envBool("AUTH_TRUST_FORWARDED_HEADER"),
	}
}

// envBool 解析布尔型环境变量,接受 1/true/yes/on(忽略大小写)为真,其余为假。
func envBool(key string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func envInt64(key string, fallback int64) int64 {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return fallback
	}
	return parsed
}

type httpPayloadLogConfig struct {
	Enabled  bool
	MaxBytes int64
}

type limitedBodyCapture struct {
	body      bytes.Buffer
	maxBytes  int64
	total     int64
	truncated bool
}

func newLimitedBodyCapture(maxBytes int64) *limitedBodyCapture {
	return &limitedBodyCapture{maxBytes: maxBytes}
}

func (capture *limitedBodyCapture) Write(data []byte) (int, error) {
	capture.total += int64(len(data))
	remaining := capture.maxBytes - int64(capture.body.Len())
	if capture.maxBytes == 0 {
		remaining = int64(len(data))
	}
	if remaining > 0 {
		writeSize := min(int64(len(data)), remaining)
		_, _ = capture.body.Write(data[:writeSize])
	}
	if capture.maxBytes > 0 && capture.total > capture.maxBytes {
		capture.truncated = true
	}
	return len(data), nil
}

func (capture *limitedBodyCapture) String() string { return capture.body.String() }

type replayReadCloser struct {
	io.Reader
	io.Closer
}

// responseRecorder 包裹 http.ResponseWriter，捕获响应状态码和 body 副本。
type responseRecorder struct {
	http.ResponseWriter
	statusCode  int
	wroteHeader bool
	bodyCapture *limitedBodyCapture
}

func (rec *responseRecorder) WriteHeader(code int) {
	if rec.wroteHeader {
		return
	}
	rec.wroteHeader = true
	rec.statusCode = code
	rec.ResponseWriter.WriteHeader(code)
}

func (rec *responseRecorder) Write(b []byte) (int, error) {
	if !rec.wroteHeader {
		rec.WriteHeader(http.StatusOK)
	}
	if rec.bodyCapture != nil {
		_, _ = rec.bodyCapture.Write(b)
	}
	return rec.ResponseWriter.Write(b)
}

// Flush 透传到底层 ResponseWriter 的 Flusher 接口。
// MCP SDK 的 Streamable HTTP 使用 http.ResponseController.Flush() 推送 SSE 事件,
// 而 ResponseController 会通过 Unwrap() 或类型断言找到 http.Flusher。
// 如果不实现,流式响应(包括 tools/list)就无法发送到客户端。
func (rec *responseRecorder) Flush() {
	if f, ok := rec.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap 返回底层 ResponseWriter,供 http.ResponseController 使用。
func (rec *responseRecorder) Unwrap() http.ResponseWriter {
	return rec.ResponseWriter
}

// requestLoggingMiddleware 始终记录请求/响应元信息；仅在配置开启时记录 Body/结果。
func requestLoggingMiddleware(next http.Handler, config httpPayloadLogConfig) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		startedAt := time.Now()
		ip := auth.ClientIP(r)

		// 提取关键请求头
		contentType := r.Header.Get("Content-Type")
		accept := r.Header.Get("Accept")
		sessionID := r.Header.Get("Mcp-Session-Id")
		protocolVersion := r.Header.Get("Mcp-Protocol-Version")
		mcpMethod := r.Header.Get("Mcp-Method")
		hasAuth := r.Header.Get("Authorization") != ""

		requestFields := map[string]any{
			"method":               r.Method,
			"path":                 r.URL.RequestURI(),
			"remote_ip":            ip,
			"host":                 r.Host,
			"content_type":         contentType,
			"accept":               accept,
			"mcp_protocol_version": protocolVersion,
			"mcp_session_id":       sessionID,
			"mcp_method":           mcpMethod,
			"has_auth":             hasAuth,
			"request_headers":      safeRequestHeaders(r.Header),
		}
		if config.Enabled && r.Body != nil && r.Body != http.NoBody {
			originalBody := r.Body
			reader := io.Reader(originalBody)
			if config.MaxBytes > 0 {
				reader = io.LimitReader(originalBody, config.MaxBytes+1)
			}
			prefix, readErr := io.ReadAll(reader)
			capture := newLimitedBodyCapture(config.MaxBytes)
			_, _ = capture.Write(prefix)
			r.Body = &replayReadCloser{
				Reader: io.MultiReader(bytes.NewReader(prefix), originalBody),
				Closer: originalBody,
			}
			requestFields["request_body"] = capture.String()
			requestFields["request_body_truncated"] = capture.truncated
			requestFields["request_body_logged_bytes"] = capture.body.Len()
			if readErr != nil {
				requestFields["request_body_read_error"] = readErr.Error()
			}
		}
		logger.HTTPCtx(r.Context(), "request", requestFields)

		var responseCapture *limitedBodyCapture
		if config.Enabled {
			responseCapture = newLimitedBodyCapture(config.MaxBytes)
		}
		rec := &responseRecorder{ResponseWriter: w, statusCode: http.StatusOK, bodyCapture: responseCapture}
		next.ServeHTTP(rec, r)

		responseFields := map[string]any{
			"method":     r.Method,
			"path":       r.URL.RequestURI(),
			"status":     rec.statusCode,
			"latency_ms": time.Since(startedAt).Milliseconds(),
		}
		if responseCapture != nil {
			responseFields["response_body"] = responseCapture.String()
			responseFields["response_body_truncated"] = responseCapture.truncated
			responseFields["response_body_bytes"] = responseCapture.total
			responseFields["response_body_logged_bytes"] = responseCapture.body.Len()
		}
		logger.HTTPCtx(r.Context(), "response", responseFields)
	})
}

func safeRequestHeaders(headers http.Header) map[string][]string {
	safe := make(map[string][]string, len(headers))
	for key, values := range headers {
		copied := append([]string(nil), values...)
		if sensitiveRequestHeader(key) {
			for i := range copied {
				copied[i] = "[REDACTED]"
			}
		}
		safe[http.CanonicalHeaderKey(key)] = copied
	}
	return safe
}

func sensitiveRequestHeader(key string) bool {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "authorization", "proxy-authorization", "cookie", "set-cookie", "x-api-key", "api-key":
		return true
	default:
		return false
	}
}

// userIDMiddleware 从 URL query string (?userId=xxx) 读取 userId,
// 注入 context,使 MCP 工具处理器可通过 trace.UserIDFromContext(ctx) 获取。
// 优先级:URL param > 已有 context 值(若未来有其他注入来源)。
func userIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if uid := r.URL.Query().Get("userId"); uid != "" {
			// 校验 userId 是否包含特殊字符或超长，防范 URL 注入或 SSRF
			if len(uid) > 128 || strings.ContainsAny(uid, "&=\r\n?#%") {
				logger.Errorf("[Auth] userId 包含非法字符或长度超限 (length=%d, raw=%q)", len(uid), uid)
				http.Error(w, "invalid userId parameter", http.StatusBadRequest)
				return
			}
			r = r.WithContext(trace.WithUserID(r.Context(), uid))
		}
		next.ServeHTTP(w, r)
	})
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
