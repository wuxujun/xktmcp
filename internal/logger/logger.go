package logger

import (
	"context"
	"fmt"
	"io"
	"log"
	"log/slog"
	"runtime"
	"time"
)

var defaultLogger *slog.Logger

// Init 初始化全局日志输出并重定向标准 log
func Init(w io.Writer) {
	// 启用 Source 选项以输出源文件和行号
	handler := slog.NewJSONHandler(w, &slog.HandlerOptions{
		AddSource: true,
	})
	defaultLogger = slog.New(handler)
	slog.SetDefault(defaultLogger)

	// 重定向 Go 标准库的 log 包输出到 slog
	log.SetOutput(slog.NewLogLogger(defaultLogger.Handler(), slog.LevelInfo).Writer())
	// 清除标准库 log 默认的日期/时间/文件名标记，由 slog 的 JSON 格式统一提供
	log.SetFlags(0)
}

func logWithCaller(level slog.Level, msg string, args ...slog.Attr) {
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
	_ = defaultLogger.Handler().Handle(context.Background(), r)
}

// Infof 普通信息日志
func Infof(format string, v ...any) {
	logWithCaller(slog.LevelInfo, fmt.Sprintf(format, v...))
}

// Errorf 错误日志
func Errorf(format string, v ...any) {
	logWithCaller(slog.LevelError, fmt.Sprintf(format, v...))
}

// Toolf 工具调用相关日志
func Toolf(toolName string, format string, v ...any) {
	logWithCaller(slog.LevelInfo, fmt.Sprintf(format, v...), slog.String("category", "tool"), slog.String("tool_name", toolName))
}

// APIf 外部 API 调用相关日志
func APIf(apiName string, format string, v ...any) {
	logWithCaller(slog.LevelInfo, fmt.Sprintf(format, v...), slog.String("category", "api"), slog.String("api_name", apiName))
}
