package logger

import (
	"context"
	"fmt"
	"io"
	"log"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/wuxujun/xktmcp/internal/trace"
)

var defaultLogger *slog.Logger

// traceHandler 包裹底层 slog.Handler,在每条日志写出前,从 context 取出 trace id
// 并作为 trace_id 字段注入——这样无需改动各处日志的格式串,只要调用方传入带 trace id
// 的 context(*Ctx 系列),同一请求的日志即可凭 trace_id 串联。
type traceHandler struct{ slog.Handler }

func (h traceHandler) Handle(ctx context.Context, r slog.Record) error {
	if id := trace.IDFromContext(ctx); id != "" {
		r.AddAttrs(slog.String("trace_id", id))
	}
	return h.Handler.Handle(ctx, r)
}

func (h traceHandler) WithAttrs(as []slog.Attr) slog.Handler {
	return traceHandler{h.Handler.WithAttrs(as)}
}

func (h traceHandler) WithGroup(name string) slog.Handler {
	return traceHandler{h.Handler.WithGroup(name)}
}

const modulePrefix = "github.com/wuxujun/xktmcp/"

// cleanSourceFile cleans absolute path prefixes and module path prefixes to ensure relative paths.
func cleanSourceFile(file string, wd string, modulePrefix string) string {
	if wd != "" {
		if trimmed, ok := strings.CutPrefix(file, wd); ok {
			file = trimmed
		}
	}
	// Trim module prefix from file path (useful when built with -trimpath)
	file = strings.TrimPrefix(file, modulePrefix)

	// If the file path is still absolute (e.g., built without -trimpath and run in a different directory),
	// try to trim the absolute prefix up to the project folder name.
	if filepath.IsAbs(file) {
		projName := filepath.Base(strings.TrimSuffix(modulePrefix, "/"))
		if projName != "" && projName != "." {
			for _, sep := range []string{"/", "\\"} {
				searchStr := sep + projName + sep
				if idx := strings.LastIndex(file, searchStr); idx != -1 {
					file = file[idx+len(searchStr):]
					break
				}
			}
		}
	}
	return file
}

// Init 初始化全局日志输出并重定向标准 log
func Init(w io.Writer) {
	wd, _ := os.Getwd()
	if wd != "" && !strings.HasSuffix(wd, string(filepath.Separator)) {
		wd += string(filepath.Separator)
	}

	// 启用 Source 选项以输出源文件和行号
	handler := traceHandler{slog.NewJSONHandler(w, &slog.HandlerOptions{
		AddSource: true,
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if a.Key == slog.SourceKey {
				if source, ok := a.Value.Any().(*slog.Source); ok {
					source.File = cleanSourceFile(source.File, wd, modulePrefix)
					source.Function = strings.TrimPrefix(source.Function, modulePrefix)
				}
			}
			return a
		},
	})}
	defaultLogger = slog.New(handler)
	slog.SetDefault(defaultLogger)

	// 重定向 Go 标准库的 log 包输出到 slog
	log.SetOutput(slog.NewLogLogger(defaultLogger.Handler(), slog.LevelInfo).Writer())
	// 清除标准库 log 默认的日期/时间/文件名标记，由 slog 的 JSON 格式统一提供
	log.SetFlags(0)
}

func logWithCaller(ctx context.Context, level slog.Level, msg string, args ...slog.Attr) {
	if defaultLogger == nil {
		return
	}
	var pcs [1]uintptr
	// 跳过 runtime.Callers、logWithCaller 以及包装层（如 Infof/Errorf）
	runtime.Callers(3, pcs[:])
	pc := pcs[0]

	r := slog.NewRecord(time.Now(), level, msg, pc)
	for _, attr := range args {
		r.AddAttrs(attr)
	}
	_ = defaultLogger.Handler().Handle(ctx, r)
}

// Infof 普通信息日志
func Infof(format string, v ...any) {
	logWithCaller(context.Background(), slog.LevelInfo, fmt.Sprintf(format, v...))
}

// Errorf 错误日志
func Errorf(format string, v ...any) {
	logWithCaller(context.Background(), slog.LevelError, fmt.Sprintf(format, v...))
}

// Toolf 工具调用相关日志
func Toolf(toolName string, format string, v ...any) {
	logWithCaller(context.Background(), slog.LevelInfo, fmt.Sprintf(format, v...), slog.String("category", "tool"), slog.String("tool_name", toolName))
}

// APIf 外部 API 调用相关日志
func APIf(apiName string, format string, v ...any) {
	logWithCaller(context.Background(), slog.LevelInfo, fmt.Sprintf(format, v...), slog.String("category", "api"), slog.String("api_name", apiName))
}

// InfofCtx 带 context 的信息日志,自动注入 trace_id。
func InfofCtx(ctx context.Context, format string, v ...any) {
	logWithCaller(ctx, slog.LevelInfo, fmt.Sprintf(format, v...))
}

// ErrorfCtx 带 context 的错误日志,自动注入 trace_id。
func ErrorfCtx(ctx context.Context, format string, v ...any) {
	logWithCaller(ctx, slog.LevelError, fmt.Sprintf(format, v...))
}

// ToolfCtx 带 context 的工具调用日志,自动注入 trace_id。
func ToolfCtx(ctx context.Context, toolName string, format string, v ...any) {
	logWithCaller(ctx, slog.LevelInfo, fmt.Sprintf(format, v...), slog.String("category", "tool"), slog.String("tool_name", toolName))
}

// APIfCtx 带 context 的外部 API 调用日志,自动注入 trace_id。
func APIfCtx(ctx context.Context, apiName string, format string, v ...any) {
	logWithCaller(ctx, slog.LevelInfo, fmt.Sprintf(format, v...), slog.String("category", "api"), slog.String("api_name", apiName))
}

// AuditCtx 写一条结构化审计日志(category=audit),记录「谁查了谁、用哪个工具、结果如何」。
// fields 里的键值会作为独立 JSON 字段输出;trace_id 由 context 自动注入。
// 调用方须确保 fields 中不含未脱敏的明文 PII(手机号/证件号应先经 pii 包处理)。
func AuditCtx(ctx context.Context, fields map[string]any) {
	attrs := make([]slog.Attr, 0, len(fields)+1)
	attrs = append(attrs, slog.String("category", "audit"))
	for k, v := range fields {
		attrs = append(attrs, slog.Any(k, v))
	}
	logWithCaller(ctx, slog.LevelInfo, "audit", attrs...)
}
