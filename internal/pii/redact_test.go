package pii

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRedactPhone(t *testing.T) {
	cases := map[string]string{
		"手机号13812345678请联系": "手机号138****5678请联系",
		"13987654321":         "139****4321",
		// 非手机号样式不动:座机、短号、12 位数字。
		"010-88886666":  "010-88886666",
		"100861234":     "100861234",
		"138123456789":  "138123456789", // 12 位,不匹配 11 位手机
	}
	for in, want := range cases {
		if got := Redact(in); got != want {
			t.Errorf("Redact(%q)=%q, 期望 %q", in, got, want)
		}
	}
}

func TestRedactIDCard(t *testing.T) {
	// 18 位身份证(末位 X)。
	if got := Redact("证件110101199003078888"); !strings.Contains(got, "***") || strings.Contains(got, "199003078888") {
		t.Errorf("18 位身份证应被掩码,得到 %q", got)
	}
	if got := Redact("11010119900307888X"); got == "11010119900307888X" {
		t.Errorf("末位 X 的 18 位身份证应被掩码,得到 %q", got)
	}
	// 15 位旧身份证。
	if got := Redact("110101900307888"); got == "110101900307888" {
		t.Errorf("15 位身份证应被掩码,得到 %q", got)
	}
}

func TestMaskIDRuneSafe(t *testing.T) {
	// 中文姓名按 rune 掩码,不应产生乱码(非法 UTF-8)。
	got := MaskID("张三丰李")
	if !utf8Valid(got) {
		t.Errorf("中文掩码结果应为合法 UTF-8,得到 %q", got)
	}
	// ASCII 标识符保留首尾。
	if got := MaskID("SMP00012345"); got != "SM*******45" {
		t.Errorf("MaskID(SMP00012345)=%q, 期望 SM*******45", got)
	}
	// 过短全掩。
	if got := MaskID("ab"); got != "**" {
		t.Errorf("MaskID(ab)=%q, 期望 **", got)
	}
}

func TestMaskSubject(t *testing.T) {
	// 手机号 → 掩手机。
	if got := MaskSubject("13812345678"); got != "138****5678" {
		t.Errorf("MaskSubject(phone)=%q, 期望 138****5678", got)
	}
	// 纯标识符 → 部分掩码。
	if got := MaskSubject("100123"); got != "1****3" {
		t.Errorf("MaskSubject(100123)=%q, 期望 1****3", got)
	}
	// 空串。
	if got := MaskSubject("   "); got != "(empty)" {
		t.Errorf("MaskSubject(空)=%q, 期望 (empty)", got)
	}
}

// RedactJSON:掩手机号/证件号,但保留 id/smp_id 等标识符(查询链路必需)。
func TestRedactJSONKeepsIdentifiers(t *testing.T) {
	type stu struct {
		ID    int    `json:"id"`
		SmpID string `json:"smp_id"`
		Name  string `json:"stu_name"`
		Phone string `json:"phone"`
	}
	in := stu{ID: 42, SmpID: "SMP10086", Name: "张三", Phone: "13812345678"}

	text, structured := RedactJSON(in)

	if !strings.Contains(text, `"id": 42`) {
		t.Errorf("数字 id 应保留,得到: %s", text)
	}
	if !strings.Contains(text, "SMP10086") {
		t.Errorf("smp_id 标识符应保留,得到: %s", text)
	}
	if !strings.Contains(text, "张三") {
		t.Errorf("姓名应保留(响应需要),得到: %s", text)
	}
	if strings.Contains(text, "13812345678") {
		t.Errorf("手机号必须被掩码,得到: %s", text)
	}
	if !strings.Contains(text, "138****5678") {
		t.Errorf("手机号应掩码为 138****5678,得到: %s", text)
	}
	// 结构化值可反序列化且同样脱敏。
	b, _ := json.Marshal(structured)
	if strings.Contains(string(b), "13812345678") {
		t.Errorf("结构化结果也应脱敏,得到: %s", b)
	}
}

func utf8Valid(s string) bool {
	for _, r := range s {
		if r == '�' {
			return false
		}
	}
	return true
}
