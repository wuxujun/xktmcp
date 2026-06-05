package tools

import "github.com/google/jsonschema-go/jsonschema"

// envelopeFields 是上游编排层(n8n)透传的内部信封字段(对应 CommonArgs)。
// 它们由结构体保留用于反序列化(例如 rag_search 仍需 userId),但【不应】出现在
// 对 LLM 公布的工具 input schema 中——否则会污染模型的参数视野、诱导其错误填值。
var envelopeFields = []string{"sessionId", "action", "chatInput", "toolCallId", "userId"}

// publicSchema 基于入参类型 T 推断完整 schema,再剔除 drop 中列出的字段,
// 得到一份"只暴露真实业务参数"的对外 schema。
//
// 关键点:把 AdditionalProperties 置 nil(解除 go-sdk 默认的 additionalProperties:false),
// 这样即便 n8n 在请求里继续透传 sessionId 等信封字段,也不会在 schema 校验阶段被拒绝,
// 从而既净化了 LLM 视野,又不破坏既有 n8n 调用。
func publicSchema[T any](drop []string) *jsonschema.Schema {
	s, err := jsonschema.For[T](&jsonschema.ForOptions{})
	if err != nil {
		// 入参类型在编译期固定,推断失败属于不可恢复的编程错误。
		panic("tools: infer input schema: " + err.Error())
	}

	dropSet := make(map[string]bool, len(drop))
	for _, f := range drop {
		dropSet[f] = true
		delete(s.Properties, f)
	}

	if len(s.Required) > 0 {
		kept := s.Required[:0]
		for _, r := range s.Required {
			if !dropSet[r] {
				kept = append(kept, r)
			}
		}
		s.Required = kept
	}

	// 解除 additionalProperties:false,放行上游透传的信封字段。
	s.AdditionalProperties = nil
	return s
}
