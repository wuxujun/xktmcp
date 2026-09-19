package main

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/joho/godotenv"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/wuxujun/xktmcp/internal/auth"
	"github.com/wuxujun/xktmcp/internal/logger"
	"github.com/wuxujun/xktmcp/internal/metrics"
	"github.com/wuxujun/xktmcp/internal/pii"
	mcp_server "github.com/wuxujun/xktmcp/internal/server"
	"github.com/wuxujun/xktmcp/internal/trace"
	"gopkg.in/natefinch/lumberjack.v2"
)

// version is replaced by release builds via -ldflags "-X main.version=...".
var version = "1.0.1"

const mcpRequestBodyReadTimeout = 30 * time.Second

func implementationVersion() string {
	if v := strings.TrimSpace(version); v != "" {
		return v
	}
	return "dev"
}

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
		Version: implementationVersion(),
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
	authCfg, err := buildAuthConfig(localToken)
	if err != nil {
		logger.Errorf("认证配置非法: %v", err)
		os.Exit(1)
	}
	authenticator, err := auth.New(authCfg)
	if err != nil {
		logger.Errorf("认证器初始化失败: %v", err)
		os.Exit(1)
	}
	var ready atomic.Bool
	ready.Store(true)

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
		bindings := newSessionBindings()
		finalHandler := authenticator.Middleware(sseSessionBindingMiddleware(sseHandler, bindings))

		mux := http.NewServeMux()
		// 健康检查端点(免认证,供探针使用)
		mux.HandleFunc("/health", healthHandler)
		mux.Handle("/ready", readinessHandler(ready.Load))
		// Prometheus 指标端点(免认证,供抓取;如需保护可置于网络隔离或反代后)
		mux.Handle("/metrics", metricsAuthHandler(metrics.Handler()))
		// 客户端连接 /sse 路径来建立事件流
		mux.Handle("/sse", userIDMiddleware(finalHandler))
		// 客户端通过 POST /messages/... 发送 JSON-RPC 消息
		mux.Handle("/messages/", userIDMiddleware(finalHandler))

		addr := fmt.Sprintf(":%d", *port)
		logger.Infof("正在通过 SSE 启动 xkt-student-server，监听地址 %s/sse...", addr)
		runServer(addr, requestBodyReadTimeoutMiddleware(requestLoggingMiddleware(mux, payloadLogConfig), mcpRequestBodyReadTimeout))

	case "http":
		// 创建 Streamable HTTP 处理器
		handler := newStreamableHTTPHandler(s)

		requireAuth(authenticator, "http")
		bindings := newSessionBindings()
		finalHandler := authenticator.Middleware(streamableSessionBindingMiddleware(handler, bindings))

		mux := http.NewServeMux()
		// 健康检查端点(免认证,供探针使用)
		mux.HandleFunc("/health", healthHandler)
		mux.Handle("/ready", readinessHandler(ready.Load))
		// Prometheus 指标端点(免认证,供抓取;如需保护可置于网络隔离或反代后)
		mux.Handle("/metrics", metricsAuthHandler(metrics.Handler()))
		// Streamable HTTP 默认通过单一路径处理
		mux.Handle("/mcp", userIDMiddleware(finalHandler))

		addr := fmt.Sprintf(":%d", *port)
		logger.Infof("正在通过 Streamable HTTP 启动 xkt-mcp-server，监听地址 %s/mcp...", addr)
		runServer(addr, requestBodyReadTimeoutMiddleware(requestLoggingMiddleware(mux, payloadLogConfig), mcpRequestBodyReadTimeout))

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
func buildAuthConfig(localToken string) (auth.Config, error) {
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
			return auth.Config{}, fmt.Errorf("parse AUTH_IP_ALLOWLIST: %w", err)
		}
		cidrs = parsed
	}

	// 解析多租户配置
	var tenants []auth.TenantConfig
	tenantConfig := strings.TrimSpace(os.Getenv("AUTH_TENANTS"))
	if tenantConfig != "" {
		if err := json.Unmarshal([]byte(tenantConfig), &tenants); err != nil {
			return auth.Config{}, fmt.Errorf("parse AUTH_TENANTS: %w", err)
		}
	}
	remoteCacheMaxEntries, err := envPositiveInt("AUTH_REMOTE_CACHE_MAX_ENTRIES", 4096)
	if err != nil {
		return auth.Config{}, err
	}
	positiveTTL, err := envPositiveDuration("AUTH_REMOTE_CACHE_POSITIVE_TTL")
	if err != nil {
		return auth.Config{}, err
	}
	negativeTTL, err := envPositiveDuration("AUTH_REMOTE_CACHE_NEGATIVE_TTL")
	if err != nil {
		return auth.Config{}, err
	}

	return auth.Config{
		LocalToken:            localToken,
		Tenants:               tenants,
		TenantsConfigured:     tenantConfig != "",
		RemoteVerifyURL:       strings.TrimSpace(os.Getenv("AUTH_REMOTE_VERIFY_URL")),
		AllowedHosts:          allowed,
		AllowedCIDRs:          cidrs,
		TrustForwardedHeader:  envBool("AUTH_TRUST_FORWARDED_HEADER"),
		RemoteCacheMaxEntries: remoteCacheMaxEntries,
		PositiveTTL:           positiveTTL,
		NegativeTTL:           negativeTTL,
	}, nil
}

func envPositiveDuration(key string) (time.Duration, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		if err == nil {
			err = fmt.Errorf("must be positive")
		}
		return 0, fmt.Errorf("invalid %s: %w", key, err)
	}
	return d, nil
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

func envPositiveInt(key string, fallback int) (int, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer", key)
	}
	return value, nil
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

type deadlineBody struct {
	io.ReadCloser
	remaining int64
	complete  bool
}

func (body *deadlineBody) Read(p []byte) (int, error) {
	n, err := body.ReadCloser.Read(p)
	if body.remaining >= 0 {
		body.remaining -= int64(n)
	}
	if err == io.EOF || body.remaining == 0 {
		body.complete = true
	}
	return n, err
}

type deadlineResponseWriter struct {
	http.ResponseWriter
	body        *deadlineBody
	wroteHeader bool
}

func (w *deadlineResponseWriter) WriteHeader(statusCode int) {
	if w.wroteHeader {
		return
	}
	w.wroteHeader = true
	if !w.body.complete {
		w.Header().Set("Connection", "close")
	}
	w.ResponseWriter.WriteHeader(statusCode)
}

func (w *deadlineResponseWriter) Write(p []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(p)
}

func (w *deadlineResponseWriter) Flush() {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	_ = http.NewResponseController(w.ResponseWriter).Flush()
}

func (w *deadlineResponseWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

// requestBodyReadTimeoutMiddleware limits how long POST clients may spend sending a body.
// GET streams remain unbounded so long-lived SSE connections are unaffected.
func requestBodyReadTimeoutMiddleware(next http.Handler, timeout time.Duration) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || timeout <= 0 {
			next.ServeHTTP(w, r)
			return
		}

		controller := http.NewResponseController(w)
		if err := controller.SetReadDeadline(time.Now().Add(timeout)); err != nil {
			logger.ErrorfCtx(r.Context(), "设置请求体读取超时失败: %v", err)
			http.Error(w, "request body timeout unavailable", http.StatusInternalServerError)
			return
		}
		requestBody := r.Body
		if requestBody == nil {
			requestBody = http.NoBody
		}
		body := &deadlineBody{
			ReadCloser: requestBody,
			remaining:  r.ContentLength,
			complete:   requestBody == http.NoBody || r.ContentLength == 0,
		}
		r.Body = body
		defer func() {
			if !body.complete {
				return
			}
			if err := controller.SetReadDeadline(time.Time{}); err != nil {
				logger.ErrorfCtx(r.Context(), "清除请求体读取超时失败: %v", err)
			}
		}()

		next.ServeHTTP(&deadlineResponseWriter{ResponseWriter: w, body: body}, r)
	})
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
			captureMaxBytes := config.MaxBytes
			readMaxBytes := config.MaxBytes
			// Authentication rejects HTTP MCP POST payloads beyond this boundary. Do
			// not let the outer logger consume more than it before Auth runs, even
			// when zero would otherwise mean unlimited payload logging.
			if r.Method == http.MethodPost && (readMaxBytes == 0 || readMaxBytes > protocolDetectionMaxBytes) {
				readMaxBytes = protocolDetectionMaxBytes
				captureMaxBytes = protocolDetectionMaxBytes
			}
			if readMaxBytes > 0 {
				reader = io.LimitReader(originalBody, readMaxBytes+1)
			}
			prefix, readErr := io.ReadAll(reader)
			capture := newLimitedBodyCapture(captureMaxBytes)
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

func metricsAuthHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		expected := strings.TrimSpace(os.Getenv("METRICS_AUTH_TOKEN"))
		if expected != "" {
			provided := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
			if subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) != 1 {
				w.Header().Set("WWW-Authenticate", `Bearer realm="metrics"`)
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// readinessHandler 返回服务是否已完成工具与认证器初始化。
func readinessHandler(ready func() bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if ready == nil || !ready() {
			http.Error(w, "not ready", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ready"}`))
	})
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

type sessionTransport string

const (
	streamableSessionTransport sessionTransport = "streamable"
	sseSessionTransport        sessionTransport = "sse"
	maxSSEEndpointEventBytes                    = 4096
)

type sseRejectionReason string

const (
	sseRejectInvalidEndpointEvent sseRejectionReason = "invalid_endpoint_event"
	sseRejectOversizedEvent       sseRejectionReason = "endpoint_event_too_large"
	sseRejectMissingSessionID     sseRejectionReason = "missing_session_id"
	sseRejectMissingIdentity      sseRejectionReason = "missing_identity"
	sseRejectSessionBinding       sseRejectionReason = "session_binding_failed"
	sseRejectMissingEndpointEvent sseRejectionReason = "missing_endpoint_event"
)

var (
	errInvalidSSEEndpointEvent = errors.New("invalid SSE endpoint event")
	errSSEEndpointNoSessionID  = errors.New("SSE endpoint event has no session ID")
)

type sessionBindingKey struct {
	transport sessionTransport
	sessionID string
}

type sessionBindings struct {
	mu    sync.Mutex
	items map[sessionBindingKey]auth.SessionIdentity
}

func newSessionBindings() *sessionBindings {
	return &sessionBindings{items: make(map[sessionBindingKey]auth.SessionIdentity)}
}

func (b *sessionBindings) bind(transport sessionTransport, sessionID string, identity auth.SessionIdentity) bool {
	if sessionID == "" || !identity.Equal(identity) {
		return false
	}

	key := sessionBindingKey{transport: transport, sessionID: sessionID}
	b.mu.Lock()
	bound, exists := b.items[key]
	if !exists {
		b.items[key] = identity
		b.mu.Unlock()
		return true
	}
	b.mu.Unlock()
	return bound.Equal(identity)
}

func (b *sessionBindings) matches(transport sessionTransport, sessionID string, identity auth.SessionIdentity) bool {
	key := sessionBindingKey{transport: transport, sessionID: sessionID}
	b.mu.Lock()
	bound, exists := b.items[key]
	b.mu.Unlock()
	return exists && bound.Equal(identity)
}

func (b *sessionBindings) delete(transport sessionTransport, sessionID string) {
	key := sessionBindingKey{transport: transport, sessionID: sessionID}
	b.mu.Lock()
	delete(b.items, key)
	b.mu.Unlock()
}

type streamableBindingWriter struct {
	http.ResponseWriter
	bindings    *sessionBindings
	identity    auth.SessionIdentity
	hasIdentity bool
	committed   bool
	blocked     bool
	statusCode  int
}

func newStreamableBindingWriter(
	w http.ResponseWriter,
	bindings *sessionBindings,
	identity auth.SessionIdentity,
	hasIdentity bool,
) *streamableBindingWriter {
	return &streamableBindingWriter{
		ResponseWriter: w,
		bindings:       bindings,
		identity:       identity,
		hasIdentity:    hasIdentity,
	}
}

func (w *streamableBindingWriter) Header() http.Header {
	return w.ResponseWriter.Header()
}

func (w *streamableBindingWriter) WriteHeader(statusCode int) {
	if w.committed {
		return
	}
	w.committed = true

	sessionID := strings.TrimSpace(w.Header().Get("Mcp-Session-Id"))
	if sessionID == "" {
		w.statusCode = statusCode
		w.ResponseWriter.WriteHeader(statusCode)
		return
	}
	if !w.hasIdentity || !w.bindings.bind(streamableSessionTransport, sessionID, w.identity) {
		w.blocked = true
		w.statusCode = http.StatusForbidden
		w.Header().Del("Mcp-Session-Id")
		http.Error(w.ResponseWriter, "Forbidden", http.StatusForbidden)
		return
	}
	w.statusCode = statusCode
	w.ResponseWriter.WriteHeader(statusCode)
}

func (w *streamableBindingWriter) Write(p []byte) (int, error) {
	if !w.committed {
		w.WriteHeader(http.StatusOK)
	}
	if w.blocked {
		return len(p), nil
	}
	return w.ResponseWriter.Write(p)
}

func (w *streamableBindingWriter) Flush() {
	if !w.committed {
		w.WriteHeader(http.StatusOK)
	}
	_ = http.NewResponseController(w.ResponseWriter).Flush()
}

func (w *streamableBindingWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func streamableSessionBindingMiddleware(next http.Handler, bindings *sessionBindings) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		identity, hasIdentity := auth.SessionIdentityFromContext(r.Context())
		sessionID := strings.TrimSpace(r.Header.Get("Mcp-Session-Id"))
		if sessionID != "" && (!hasIdentity || !bindings.matches(streamableSessionTransport, sessionID, identity)) {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		writer := newStreamableBindingWriter(w, bindings, identity, hasIdentity)
		next.ServeHTTP(writer, r)
		if r.Method == http.MethodDelete && sessionID != "" &&
			(writer.statusCode == 0 || writer.statusCode >= 200 && writer.statusCode < 300) {
			bindings.delete(streamableSessionTransport, sessionID)
		}
	})
}

type sseEndpointBindingWriter struct {
	http.ResponseWriter
	bindings    *sessionBindings
	identity    auth.SessionIdentity
	hasIdentity bool
	buffer      []byte
	statusCode  int
	sessionID   string
	ready       bool
	blocked     bool
}

func newSSEEndpointBindingWriter(
	w http.ResponseWriter,
	bindings *sessionBindings,
	identity auth.SessionIdentity,
	hasIdentity bool,
) *sseEndpointBindingWriter {
	return &sseEndpointBindingWriter{
		ResponseWriter: w,
		bindings:       bindings,
		identity:       identity,
		hasIdentity:    hasIdentity,
		buffer:         make([]byte, 0, maxSSEEndpointEventBytes),
	}
}

func (w *sseEndpointBindingWriter) Header() http.Header {
	return w.ResponseWriter.Header()
}

func (w *sseEndpointBindingWriter) WriteHeader(statusCode int) {
	if w.blocked || w.ready || w.statusCode != 0 {
		return
	}
	w.statusCode = statusCode
}

func (w *sseEndpointBindingWriter) Write(p []byte) (int, error) {
	if w.blocked {
		return len(p), nil
	}
	if w.ready {
		return w.writeResponse(p)
	}

	bufferedBefore := len(w.buffer)
	for i, b := range p {
		w.buffer = append(w.buffer, b)
		if len(w.buffer) > maxSSEEndpointEventBytes {
			w.reject("", sseRejectOversizedEvent)
			return len(p), nil
		}
		if !hasSSEEventBoundary(w.buffer) {
			continue
		}

		sessionID, err := parseSSEEndpointSessionID(w.buffer)
		if err != nil {
			reason := sseRejectInvalidEndpointEvent
			if errors.Is(err, errSSEEndpointNoSessionID) {
				reason = sseRejectMissingSessionID
			}
			w.reject("", reason)
			return len(p), nil
		}
		if !w.hasIdentity {
			w.reject(sessionID, sseRejectMissingIdentity)
			return len(p), nil
		}
		if !w.bindings.bind(sseSessionTransport, sessionID, w.identity) {
			w.reject(sessionID, sseRejectSessionBinding)
			return len(p), nil
		}

		w.sessionID = sessionID
		w.commitStatus()
		if n, err := w.writeResponse(w.buffer); err != nil {
			n -= bufferedBefore
			if n < 0 {
				n = 0
			}
			if n > i+1 {
				n = i + 1
			}
			return n, err
		}
		w.buffer = nil
		w.ready = true
		if i+1 < len(p) {
			n, err := w.writeResponse(p[i+1:])
			return i + 1 + n, err
		}
		return len(p), nil
	}

	return len(p), nil
}

func (w *sseEndpointBindingWriter) writeResponse(p []byte) (int, error) {
	n, err := w.ResponseWriter.Write(p)
	if n < len(p) && err == nil {
		err = io.ErrShortWrite
	}
	if err != nil {
		w.blocked = true
		w.ready = false
		w.buffer = nil
	}
	return n, err
}

func (w *sseEndpointBindingWriter) Flush() {
	if w.blocked || !w.ready {
		return
	}
	_ = http.NewResponseController(w.ResponseWriter).Flush()
}

func (w *sseEndpointBindingWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func (w *sseEndpointBindingWriter) commitStatus() {
	if w.statusCode == 0 {
		w.statusCode = http.StatusOK
	}
	w.ResponseWriter.WriteHeader(w.statusCode)
}

func (w *sseEndpointBindingWriter) reject(sessionID string, reason sseRejectionReason) {
	logger.Errorf(
		"SSE session rejected: transport=%s session_id=%s reason=%s",
		sseSessionTransport,
		pii.MaskSubject(sessionID),
		reason,
	)
	w.blocked = true
	w.buffer = nil
	w.Header().Del("Cache-Control")
	w.Header().Del("Connection")
	w.Header().Del("Content-Type")
	http.Error(w.ResponseWriter, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
}

func hasSSEEventBoundary(event []byte) bool {
	return bytes.HasSuffix(event, []byte("\n\n")) || bytes.HasSuffix(event, []byte("\r\n\r\n"))
}

func parseSSEEndpointSessionID(event []byte) (string, error) {
	normalized := strings.ReplaceAll(string(event), "\r\n", "\n")
	var eventName, data string
	dataLines := 0
	for _, line := range strings.Split(strings.TrimSuffix(normalized, "\n\n"), "\n") {
		switch {
		case strings.HasPrefix(line, "event:"):
			eventName = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			data = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			dataLines++
		}
	}
	if eventName != "endpoint" || data == "" || dataLines != 1 {
		return "", errInvalidSSEEndpointEvent
	}
	u, err := url.Parse(data)
	if err != nil {
		return "", errSSEEndpointNoSessionID
	}
	sessionID := strings.TrimSpace(u.Query().Get("sessionid"))
	if sessionID == "" {
		return "", errSSEEndpointNoSessionID
	}
	return sessionID, nil
}

func sseSessionBindingMiddleware(next http.Handler, bindings *sessionBindings) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		identity, hasIdentity := auth.SessionIdentityFromContext(r.Context())
		if r.Method == http.MethodPost {
			sessionID := strings.TrimSpace(r.URL.Query().Get("sessionid"))
			if sessionID == "" || !hasIdentity || !bindings.matches(sseSessionTransport, sessionID, identity) {
				http.Error(w, "Forbidden", http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		if r.Method != http.MethodGet {
			next.ServeHTTP(w, r)
			return
		}

		writer := newSSEEndpointBindingWriter(w, bindings, identity, hasIdentity)
		defer func() {
			if writer.sessionID != "" {
				bindings.delete(sseSessionTransport, writer.sessionID)
			}
		}()
		next.ServeHTTP(writer, r)
		if !writer.ready && !writer.blocked {
			writer.reject("", sseRejectMissingEndpointEvent)
		}
	})
}
