// Package pii 提供个人敏感信息(手机号、身份证号)的识别与脱敏,以及标识符的部分掩码。
//
// 两类用途,口径不同:
//   - 响应脱敏(返回给 LLM 的数据):只掩手机号/身份证号(Redact),【保留】id/smp_id 等
//     标识符与姓名,否则会破坏「查 id → 再查订单」的链路及问答能力。
//   - 日志/审计脱敏:除手机号/身份证号外,还对自由文本(query)与标识符做部分掩码
//     (MaskSubject),最大限度减少日志里的明文 PII。
package pii

import (
	"encoding/json"
	"regexp"
	"strings"
)

var (
	// 中国大陆手机号:11 位,1 开头,第二位 3-9。用单词边界避免匹配更长数字串的一部分。
	rePhone = regexp.MustCompile(`\b1[3-9]\d{9}\b`)
	// 身份证号:18 位(末位可为 X/x)或 15 位(旧版)。
	reIDCard18 = regexp.MustCompile(`\b\d{17}[\dXx]\b`)
	reIDCard15 = regexp.MustCompile(`\b\d{15}\b`)
)

// Redact 掩码文本中所有手机号/身份证号样式的子串,其余原样保留。
// 用于返回给 LLM 的响应:既盖住直接 PII,又不动标识符/姓名,保证功能不破坏。
//
// 注意:基于正则,极少数恰好为 11/15/18 位的非 PII 数字(如某些订单号)可能被误掩;
// 这是「宁可多掩」的安全取舍。
func Redact(s string) string {
	// 先长后短,避免短模式匹配到长号码的一部分(配合单词边界已基本无碰撞)。
	s = reIDCard18.ReplaceAllStringFunc(s, maskDigits)
	s = reIDCard15.ReplaceAllStringFunc(s, maskDigits)
	s = rePhone.ReplaceAllStringFunc(s, maskDigits)
	return s
}

// maskDigits 保留前 3、后 4 位,中间用 * 填充(适配手机号 11 位与身份证 15/18 位)。
func maskDigits(s string) string {
	const head, tail = 3, 4
	if len(s) <= head+tail {
		return strings.Repeat("*", len(s))
	}
	return s[:head] + strings.Repeat("*", len(s)-head-tail) + s[len(s)-tail:]
}

// MaskID 对标识符/姓名做 rune 安全的部分掩码:保留首尾若干字符,中间星号。
// 用于日志/审计里的标识符(smp_id/userid/uaid)与短主体。
func MaskID(s string) string {
	r := []rune(strings.TrimSpace(s))
	n := len(r)
	switch {
	case n == 0:
		return ""
	case n <= 2:
		return strings.Repeat("*", n)
	case n <= 6:
		return string(r[0]) + strings.Repeat("*", n-2) + string(r[n-1])
	default:
		return string(r[:2]) + strings.Repeat("*", n-4) + string(r[n-2:])
	}
}

// MaskSubject 用于日志/审计里的「被查询主体」:先掩手机号/身份证号;若本身就是
// 标识符/姓名(无上述模式),退化为部分掩码。空串返回 "(empty)"。
func MaskSubject(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "(empty)"
	}
	if r := Redact(s); r != s {
		return r
	}
	return MaskID(s)
}

// RedactJSON 把任意值序列化为缩进 JSON 并对其做响应级脱敏(掩手机号/身份证号),
// 返回脱敏后的文本与反序列化回的结构化值(供 MCP 文本内容与结构化内容同时脱敏)。
// 序列化/反序列化失败时退回原值,保证不致命。
func RedactJSON(v any) (text string, structured any) {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "", v
	}
	red := Redact(string(raw))
	var out any
	if err := json.Unmarshal([]byte(red), &out); err != nil {
		return red, v
	}
	return red, out
}
