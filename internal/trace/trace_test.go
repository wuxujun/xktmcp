package trace

import (
	"context"
	"testing"
)

func TestNewIDFormat(t *testing.T) {
	id := NewID()
	if len(id) != 16 {
		t.Fatalf("NewID 应为 16 位十六进制,得到 %q(len=%d)", id, len(id))
	}
	// 两次生成应不同(极小概率碰撞可忽略)。
	if id == NewID() {
		t.Error("连续两次 NewID 不应相同")
	}
}

func TestWithAndFromContext(t *testing.T) {
	ctx := WithID(context.Background(), "abc123")
	if got := IDFromContext(ctx); got != "abc123" {
		t.Errorf("IDFromContext = %q,期望 abc123", got)
	}
	// 空 context 返回空串。
	if got := IDFromContext(context.Background()); got != "" {
		t.Errorf("无 id 的 context 应返回空串,得到 %q", got)
	}
	// nil context 安全。
	if got := IDFromContext(nil); got != "" { //nolint:staticcheck // 故意测 nil
		t.Errorf("nil context 应返回空串,得到 %q", got)
	}
}

func TestEnsureIDPrecedence(t *testing.T) {
	// 1) ctx 已有 id:原样返回,忽略 preferred。
	ctx := WithID(context.Background(), "existing")
	gotCtx, id := EnsureID(ctx, "n8n-tool-call", "session")
	if id != "existing" {
		t.Errorf("应优先用 ctx 中已有 id,得到 %q", id)
	}
	if IDFromContext(gotCtx) != "existing" {
		t.Error("返回的 ctx 应保留原 id")
	}

	// 2) ctx 无 id:取 preferred 首个非空。
	_, id = EnsureID(context.Background(), "", "n8n-tool-call", "session")
	if id != "n8n-tool-call" {
		t.Errorf("应取首个非空 preferred,得到 %q", id)
	}

	// 3) ctx 无 id 且 preferred 全空:生成新 id。
	gotCtx, id = EnsureID(context.Background(), "", "")
	if len(id) != 16 {
		t.Errorf("应生成 16 位新 id,得到 %q", id)
	}
	if IDFromContext(gotCtx) != id {
		t.Error("返回的 ctx 应携带新生成的 id")
	}
}
