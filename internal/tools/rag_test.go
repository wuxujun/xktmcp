package tools

import (
	"context"
	"errors"
	"testing"
)

func TestRewriteQuery(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "ends with 怎么处理",
			input:    "请假怎么处理",
			expected: "请假的处理规则",
		},
		{
			name:     "ends with 如何处理",
			input:    "加班如何处理",
			expected: "加班的处理规则",
		},
		{
			name:     "replace 后的的",
			input:    "审批后的的表单",
			expected: "审批后的表单",
		},
		{
			name:     "replace 后考勤",
			input:    "入职后考勤记录",
			expected: "入职后的考勤记录",
		},
		{
			name:     "no match",
			input:    "怎么请假",
			expected: "怎么请假",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actual := rewriteQuery(tt.input)
			if actual != tt.expected {
				t.Errorf("rewriteQuery(%q) = %q, want %q", tt.input, actual, tt.expected)
			}
		})
	}
}

func TestRewriteQuerySemanticFallback(t *testing.T) {
	// When session is nil, rewriteQuerySemantic should fallback to rewriteQuery
	actual := rewriteQuerySemantic(context.Background(), nil, "请假怎么处理")
	expected := "请假的处理规则"
	if actual != expected {
		t.Errorf("rewriteQuerySemantic(nil) = %q, want %q", actual, expected)
	}
}

func TestIsMethodNotFound(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{nil, false},
		{errors.New(`calling "sampling/createMessage": Method not found`), true},
		{errors.New("method not found"), true}, // 大小写不敏感
		{errors.New("context deadline exceeded"), false},
		{errors.New("connection refused"), false},
	}
	for _, c := range cases {
		if got := isMethodNotFound(c.err); got != c.want {
			t.Errorf("isMethodNotFound(%v) = %v, want %v", c.err, got, c.want)
		}
	}
}

func TestSamplingUnsupportedShortCircuit(t *testing.T) {
	// 置位标志后,即使 session 非 nil 的路径也不会被走到——
	// 这里用 nil session 验证短路返回本地改写结果,并确保测试后复位全局标志。
	samplingUnsupported.Store(true)
	defer samplingUnsupported.Store(false)

	got := rewriteQuerySemantic(context.Background(), nil, "请假怎么处理")
	if want := rewriteQuery("请假怎么处理"); got != want {
		t.Errorf("标志置位时应直接走本地改写: got %q, want %q", got, want)
	}
}
