// Package trace 提供请求级 trace id 的生成与 context 传递,用于把同一次工具调用
// 在各层(工具处理器 → service → client 上游请求 → 缓存)产生的日志串联起来排障。
package trace

import (
	"context"
	"crypto/rand"
	"encoding/hex"
)

type ctxKey struct{}

// NewID 生成 16 位十六进制(8 字节)随机 trace id。
// crypto/rand 极少失败,失败时退回全零 id(仍可用于串联,只是不唯一)。
func NewID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "0000000000000000"
	}
	return hex.EncodeToString(b[:])
}

// WithID 把 trace id 放入 context。
func WithID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, ctxKey{}, id)
}

// IDFromContext 取出 context 中的 trace id;不存在返回空串。
func IDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if v, ok := ctx.Value(ctxKey{}).(string); ok {
		return v
	}
	return ""
}

// EnsureID 返回带 trace id 的 context 及该 id。
// 取值优先级:ctx 中已有的 > preferred 里首个非空(如 n8n 透传的 toolCallId/sessionId)> 新生成。
func EnsureID(ctx context.Context, preferred ...string) (context.Context, string) {
	if id := IDFromContext(ctx); id != "" {
		return ctx, id
	}
	for _, p := range preferred {
		if p != "" {
			return WithID(ctx, p), p
		}
	}
	id := NewID()
	return WithID(ctx, id), id
}
