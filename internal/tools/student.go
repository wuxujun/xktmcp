package tools

import (
	"context"
	"fmt"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/wuxujun/xktmcp/internal/logger"
	"github.com/wuxujun/xktmcp/internal/metrics"
	"github.com/wuxujun/xktmcp/internal/pii"
	"github.com/wuxujun/xktmcp/internal/service"
)

var studentCache = sharedCache

// toolResultItem 缓存一次工具调用的完整结果(MCP 文本结果 + 结构化数据),
// 供 student_* 与 staff_search 等返回 {result, data} 形态的工具复用。
type toolResultItem struct {
	result *mcp.CallToolResult
	data   any
}

const studentQueryTTL = 60 * time.Second
const studentGetTTL = 5 * time.Minute

type CommonArgs struct {
	SessionID  string `json:"sessionId,omitempty"`
	Action     string `json:"action,omitempty"`
	ChatInput  string `json:"chatInput,omitempty"`
	ToolCallID string `json:"toolCallId,omitempty"`
	UserID     string `json:"userId,omitempty"`
}

// CorrelationID 返回用于请求级 trace 的关联 id:优先 n8n 透传的 toolCallId,
// 其次 sessionId;都为空则返回空串(由上层生成新 id)。所有工具 Args 均内嵌
// CommonArgs,故该方法被提升到各 Args 类型上,可用于统一埋点。
func (c CommonArgs) CorrelationID() string {
	if c.ToolCallID != "" {
		return c.ToolCallID
	}
	return c.SessionID
}

// Querier 返回发起本次查询的主体(n8n 用户 id),用于审计「谁查的」。
// 同样经 CommonArgs 提升到各 Args 类型。
func (c CommonArgs) Querier() string { return c.UserID }

type StudentSearchArgs struct {
	CommonArgs
	Query    string `json:"query" jsonschema:"查询关键字，可以输入学员姓名、手机号等模糊信息"`
	Page     int    `json:"page,omitempty" jsonschema:"页码，从 1 开始，默认为 1"`
	PageSize int    `json:"page_size,omitempty" jsonschema:"每页返回条数，默认 20，最大 100"`
}

// AuditSubject 返回被查询主体(供审计记录,会在上层脱敏后落日志)。
func (a StudentSearchArgs) AuditSubject() string { return a.Query }

type StudentGetArgs struct {
	CommonArgs
	ID string `json:"id" jsonschema:"学员的唯一 ID (对应 id 或 smp_id)"`
}

// AuditSubject 返回被查询主体(供审计记录,会在上层脱敏后落日志)。
func (a StudentGetArgs) AuditSubject() string { return a.ID }

func StudentSearchTool() *mcp.Tool {
	return &mcp.Tool{
		Name:        "student_search",
		Description: `用于根据姓名等模糊信息查询学员基本信息。当用户询问某学员的信息，或你需要获取某学员的 ID 以便后续查询其订单、考试成绩时，必须【优先调用】此工具。返回数据中包含学员的唯一标识（id / smp_id），请提取该 ID 用于后续的其他查询工具。若未找到学员，请直接告知用户"未找到该学员信息"。支持分页：page 从 1 开始，page_size 默认 20、最大 100；同名学员较多时可翻页获取更多结果。`,
		InputSchema: publicSchema[StudentSearchArgs](envelopeFields),
	}
}

func StudentOrderTool() *mcp.Tool {
	return &mcp.Tool{
		Name:        "student_order",
		Description: `用于查询特定学员的订单信息。【前置条件】此工具的 id 参数必须是精确的学员 ID (如 id 或 smp_id)。如果你当前只知道学员姓名而不知道其 ID，【必须】先调用 student_search 工具查出该学员对应的 ID，然后再将获取到的 ID 作为 id 参数调用本工具。`,
		InputSchema: publicSchema[StudentGetArgs](envelopeFields),
	}
}

func StudentExamTool() *mcp.Tool {
	return &mcp.Tool{
		Name:        "student_exam",
		Description: `用于查询特定学员的考试成绩信息。【前置条件】此工具的 id 参数必须是精确的学员 ID (如 id 或 smp_id)。如果你当前只知道学员姓名而不知道其 ID，【必须】先调用 student_search 工具查出该学员对应的 ID，然后再将获取到的 ID 作为 id 参数调用本工具。`,
		InputSchema: publicSchema[StudentGetArgs](envelopeFields),
	}
}

func StudentGetTool() *mcp.Tool {
	return &mcp.Tool{
		Name:        "student_get",
		Description: "根据精确的学员 ID (如 id 或 smp_id) 获取学员详细的档案信息。",
		InputSchema: publicSchema[StudentGetArgs](envelopeFields),
	}
}

func StudentSearchHandler(
	svc *service.StudentService,
) func(context.Context, *mcp.CallToolRequest, StudentSearchArgs) (*mcp.CallToolResult, any, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest, args StudentSearchArgs) (*mcp.CallToolResult, any, error) {
		logger.ToolfCtx(ctx, "student_search", "querier=%s subject=%s page=%d page_size=%d", args.UserID, pii.MaskSubject(args.Query), args.Page, args.PageSize)

		// 分页参数归一化
		page := args.Page
		if page <= 0 {
			page = 1
		}
		pageSize := args.PageSize
		if pageSize <= 0 {
			pageSize = 20
		} else if pageSize > 100 {
			pageSize = 100
		}

		cacheKey := fmt.Sprintf("student:search:%s:%d:%d", args.Query, page, pageSize)
		if val, ok := studentCache.Get(cacheKey); ok {
			cached := val.(toolResultItem)
			logger.InfofCtx(ctx, "[Cache] student_search hit cache: query=%s page=%d", args.Query, page)
			metrics.ObserveCacheAccess("student_search", true)
			return cached.result, cached.data, nil
		}
		metrics.ObserveCacheAccess("student_search", false)

		items, err := svc.Search(ctx, args.Query, page, pageSize)
		if err != nil {
			return &mcp.CallToolResult{
				Content: []mcp.Content{
					&mcp.TextContent{Text: fmt.Sprintf("student search failed: %v", err)},
				},
				IsError: true,
			}, nil, nil
		}

		text, redacted := pii.RedactJSON(items)
		res := &mcp.CallToolResult{
			Content: []mcp.Content{
				&mcp.TextContent{Text: text},
			},
		}
		structured := map[string]any{"items": redacted, "page": page, "page_size": pageSize}
		studentCache.Set(cacheKey, toolResultItem{result: res, data: structured}, studentQueryTTL)
		return res, structured, nil
	}
}

func StudentOrderHandler(
	svc *service.StudentService,
) func(context.Context, *mcp.CallToolRequest, StudentGetArgs) (*mcp.CallToolResult, any, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest, args StudentGetArgs) (*mcp.CallToolResult, any, error) {
		logger.ToolfCtx(ctx, "student_order", "querier=%s subject=%s", args.UserID, pii.MaskSubject(args.ID))

		cacheKey := "student:order:" + args.ID
		if val, ok := studentCache.Get(cacheKey); ok {
			cached := val.(toolResultItem)
			logger.InfofCtx(ctx, "[Cache] student_order hit cache: id=%s", args.ID)
			metrics.ObserveCacheAccess("student_order", true)
			return cached.result, cached.data, nil
		}
		metrics.ObserveCacheAccess("student_order", false)

		items, err := svc.SearchOrders(ctx, args.ID)
		if err != nil {
			return &mcp.CallToolResult{
				Content: []mcp.Content{
					&mcp.TextContent{Text: fmt.Sprintf("student order failed: %v", err)},
				},
				IsError: true,
			}, nil, nil
		}

		text, redacted := pii.RedactJSON(items)
		res := &mcp.CallToolResult{
			Content: []mcp.Content{
				&mcp.TextContent{Text: text},
			},
		}
		structured := map[string]any{"items": redacted}
		studentCache.Set(cacheKey, toolResultItem{result: res, data: structured}, studentQueryTTL)
		return res, structured, nil
	}
}

func StudentExamHandler(
	svc *service.StudentService,
) func(context.Context, *mcp.CallToolRequest, StudentGetArgs) (*mcp.CallToolResult, any, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest, args StudentGetArgs) (*mcp.CallToolResult, any, error) {
		logger.ToolfCtx(ctx, "student_exam", "querier=%s subject=%s", args.UserID, pii.MaskSubject(args.ID))

		cacheKey := "student:exam:" + args.ID
		if val, ok := studentCache.Get(cacheKey); ok {
			cached := val.(toolResultItem)
			logger.InfofCtx(ctx, "[Cache] student_exam hit cache: id=%s", args.ID)
			metrics.ObserveCacheAccess("student_exam", true)
			return cached.result, cached.data, nil
		}
		metrics.ObserveCacheAccess("student_exam", false)

		items, err := svc.SearchExam(ctx, args.ID)
		if err != nil {
			return &mcp.CallToolResult{
				Content: []mcp.Content{
					&mcp.TextContent{Text: fmt.Sprintf("student exam failed: %v", err)},
				},
				IsError: true,
			}, nil, nil
		}

		text, redacted := pii.RedactJSON(items)
		res := &mcp.CallToolResult{
			Content: []mcp.Content{
				&mcp.TextContent{Text: text},
			},
		}
		structured := map[string]any{"items": redacted}
		studentCache.Set(cacheKey, toolResultItem{result: res, data: structured}, studentQueryTTL)
		return res, structured, nil
	}
}

func StudentGetHandler(
	svc *service.StudentService,
) func(context.Context, *mcp.CallToolRequest, StudentGetArgs) (*mcp.CallToolResult, any, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest, args StudentGetArgs) (*mcp.CallToolResult, any, error) {
		logger.ToolfCtx(ctx, "student_get", "querier=%s subject=%s", args.UserID, pii.MaskSubject(args.ID))

		cacheKey := "student:get:" + args.ID
		if val, ok := studentCache.Get(cacheKey); ok {
			cached := val.(toolResultItem)
			logger.InfofCtx(ctx, "[Cache] student_get hit cache: id=%s", args.ID)
			metrics.ObserveCacheAccess("student_get", true)
			return cached.result, cached.data, nil
		}
		metrics.ObserveCacheAccess("student_get", false)

		item, err := svc.Get(ctx, args.ID)
		if err != nil {
			return &mcp.CallToolResult{
				Content: []mcp.Content{
					&mcp.TextContent{Text: fmt.Sprintf("student get failed: %v", err)},
				},
				IsError: true,
			}, nil, nil
		}

		text, redacted := pii.RedactJSON(item)
		res := &mcp.CallToolResult{
			Content: []mcp.Content{
				&mcp.TextContent{Text: text},
			},
		}

		studentCache.Set(cacheKey, toolResultItem{result: res, data: redacted}, studentGetTTL)
		return res, redacted, nil
	}
}
