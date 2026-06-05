package tools

import (
	"context"
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
