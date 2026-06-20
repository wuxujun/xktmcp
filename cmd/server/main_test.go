package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/wuxujun/xktmcp/internal/trace"
)

func TestUserIDMiddlewareInjectsQueryUserID(t *testing.T) {
	var got string
	handler := userIDMiddleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got = trace.UserIDFromContext(r.Context())
	}))

	req := httptest.NewRequest(http.MethodGet, "/sse?userId=user-123", nil)
	handler.ServeHTTP(httptest.NewRecorder(), req)

	if got != "user-123" {
		t.Fatalf("expected context userId user-123, got %q", got)
	}
}

func TestUserIDMiddlewareKeepsExistingContextWhenQueryMissing(t *testing.T) {
	var got string
	handler := userIDMiddleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got = trace.UserIDFromContext(r.Context())
	}))

	req := httptest.NewRequest(http.MethodGet, "/messages/1", nil)
	req = req.WithContext(trace.WithUserID(req.Context(), "existing-user"))
	handler.ServeHTTP(httptest.NewRecorder(), req)

	if got != "existing-user" {
		t.Fatalf("expected existing context userId, got %q", got)
	}
}
