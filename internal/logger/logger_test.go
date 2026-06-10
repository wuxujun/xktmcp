package logger

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/wuxujun/xktmcp/internal/trace"
)

// 带 trace id 的 context 经 *Ctx 日志写出时,应自动注入 trace_id 字段。
func TestCtxLoggingInjectsTraceID(t *testing.T) {
	var buf bytes.Buffer
	Init(&buf)

	ctx := trace.WithID(context.Background(), "trace-xyz")
	InfofCtx(ctx, "hello %s", "world")

	out := buf.String()
	if !strings.Contains(out, `"trace_id":"trace-xyz"`) {
		t.Errorf("带 trace id 的日志应含 trace_id 字段,实际输出: %s", out)
	}
	if !strings.Contains(out, "hello world") {
		t.Errorf("日志应含格式化后的消息,实际输出: %s", out)
	}
}

// 无 trace id 的普通日志不应出现 trace_id 字段。
func TestPlainLoggingHasNoTraceID(t *testing.T) {
	var buf bytes.Buffer
	Init(&buf)

	Infof("no trace here")

	if strings.Contains(buf.String(), "trace_id") {
		t.Errorf("无 trace id 时不应出现 trace_id 字段,实际输出: %s", buf.String())
	}
}

// ToolfCtx 应带上 category=tool 与 tool_name,以及 trace_id。
func TestToolfCtxAttributes(t *testing.T) {
	var buf bytes.Buffer
	Init(&buf)

	ctx := trace.WithID(context.Background(), "tid-1")
	ToolfCtx(ctx, "rag_search", "调用完成 status=%s", "ok")

	out := buf.String()
	for _, want := range []string{`"category":"tool"`, `"tool_name":"rag_search"`, `"trace_id":"tid-1"`} {
		if !strings.Contains(out, want) {
			t.Errorf("输出应含 %s,实际: %s", want, out)
		}
	}
}

// 验证日志中输出的 source file 路径与 function 名称为非绝对/带前缀路径 (相对路径)
func TestRelativeSourcePath(t *testing.T) {
	var buf bytes.Buffer
	Init(&buf)

	Infof("test relative path")

	out := buf.String()
	// 日志中的 source file 应该为相对路径，例如 "internal/logger/logger_test.go" 或 "logger_test.go"
	if !strings.Contains(out, `"file":"internal/logger/logger_test.go"`) && !strings.Contains(out, `"file":"logger_test.go"`) {
		t.Errorf("期望得到相对的 source file 路径, 实际输出: %s", out)
	}
	// 日志中的 function 应该移除了 module 前缀，为 "internal/logger.TestRelativeSourcePath"
	if !strings.Contains(out, `"function":"internal/logger.TestRelativeSourcePath"`) {
		t.Errorf("期望得到相对的 function 路径, 实际输出: %s", out)
	}
}

