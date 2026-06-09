package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/wuxujun/xktmcp/internal/logger"
	"github.com/wuxujun/xktmcp/internal/service"
)

var studentCache = sharedCache

// toolResultItem 缓存一次工具调用的完整结果(MCP 文本结果 + 结构化数据),
// 供 student_* 与 staff_search 等返回 {result, data} 形态的工具复用。
type toolResultItem struct {
	result *mcp.CallToolResult
	data   any
}

// studentQueryTTL 是 student_search/order/exam 这类「可变数据查询」的缓存有效期。
// 取较短值(60s):主要用于吸收同一会话内对同一对象的重复调用,同时把订单/成绩等
// 可变数据的陈旧窗口限制在可接受范围。student_get(档案,相对稳定)仍用 5min。
const studentQueryTTL = 60 * time.Second

type CommonArgs struct {
	SessionID  string `json:"sessionId,omitempty"`
	Action     string `json:"action,omitempty"`
	ChatInput  string `json:"chatInput,omitempty"`
	ToolCallID string `json:"toolCallId,omitempty"`
	UserID     string `json:"userId,omitempty"`
}

type StudentSearchArgs struct {
	CommonArgs
	Query string `json:"query" jsonschema:"查询关键字，可以输入学员姓名、手机号等模糊信息"`
}

type StudentQueryByIDArgs struct {
	CommonArgs
	Query string `json:"query" jsonschema:"精确的学员 ID (对应 id 或 smp_id)。若只有姓名，必须先用 student_search 工具查询获取 ID"`
}

type StudentGetArgs struct {
	CommonArgs
	ID string `json:"id" jsonschema:"学员的唯一 ID (对应 id 或 smp_id)"`
}

func StudentSearchTool() *mcp.Tool {
	return &mcp.Tool{
		Name:        "student_search",
		Description: `用于根据姓名等模糊信息查询学员基本信息。当用户询问某学员的信息，或你需要获取某学员的 ID 以便后续查询其订单、考试成绩时，必须【优先调用】此工具。返回数据中包含学员的唯一标识（id / smp_id），请提取该 ID 用于后续的其他查询工具。若未找到学员，请直接告知用户"未找到该学员信息"。`,
		InputSchema: publicSchema[StudentSearchArgs](envelopeFields),
	}
}

func StudentOrderTool() *mcp.Tool {
	return &mcp.Tool{
		Name:        "student_order",
		Description: `用于查询特定学员的订单信息。【前置条件】此工具的 query 参数必须是精确的学员 ID (如 id 或 smp_id)。如果你当前只知道学员姓名而不知道其 ID，【必须】先调用 student_search 工具查出该学员对应的 ID，然后再将获取到的 ID 作为 query 参数调用本工具。`,
		InputSchema: publicSchema[StudentQueryByIDArgs](envelopeFields),
	}
}

func StudentExamTool() *mcp.Tool {
	return &mcp.Tool{
		Name:        "student_exam",
		Description: `用于查询特定学员的考试成绩信息。【前置条件】此工具的 query 参数必须是精确的学员 ID (如 id 或 smp_id)。如果你当前只知道学员姓名而不知道其 ID，【必须】先调用 student_search 工具查出该学员对应的 ID，然后再将获取到的 ID 作为 query 参数调用本工具。`,
		InputSchema: publicSchema[StudentQueryByIDArgs](envelopeFields),
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
		logger.Toolf("student_search", "参数: %+v", args)

		cacheKey := "student:search:" + args.Query
		if val, ok := studentCache.Get(cacheKey); ok {
			cached := val.(toolResultItem)
			logger.Infof("[Cache] student_search hit cache: query=%s", args.Query)
			return cached.result, cached.data, nil
		}

		items, err := svc.Search(ctx, args.Query)
		if err != nil {
			return &mcp.CallToolResult{
				Content: []mcp.Content{
					&mcp.TextContent{Text: fmt.Sprintf("student search failed: %v", err)},
				},
				IsError: true,
			}, nil, nil
		}

		data, _ := json.MarshalIndent(items, "", "  ")
		res := &mcp.CallToolResult{
			Content: []mcp.Content{
				&mcp.TextContent{Text: string(data)},
			},
		}
		structured := map[string]any{"items": items}
		studentCache.Set(cacheKey, toolResultItem{result: res, data: structured}, studentQueryTTL)
		return res, structured, nil
	}
}

func StudentOrderHandler(
	svc *service.StudentService,
) func(context.Context, *mcp.CallToolRequest, StudentQueryByIDArgs) (*mcp.CallToolResult, any, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest, args StudentQueryByIDArgs) (*mcp.CallToolResult, any, error) {
		logger.Toolf("student_order", "参数: %+v", args)

		cacheKey := "student:order:" + args.Query
		if val, ok := studentCache.Get(cacheKey); ok {
			cached := val.(toolResultItem)
			logger.Infof("[Cache] student_order hit cache: id=%s", args.Query)
			return cached.result, cached.data, nil
		}

		items, err := svc.SearchOrders(ctx, args.Query)
		if err != nil {
			return &mcp.CallToolResult{
				Content: []mcp.Content{
					&mcp.TextContent{Text: fmt.Sprintf("student order failed: %v", err)},
				},
				IsError: true,
			}, nil, nil
		}

		data, _ := json.MarshalIndent(items, "", "  ")
		res := &mcp.CallToolResult{
			Content: []mcp.Content{
				&mcp.TextContent{Text: string(data)},
			},
		}
		structured := map[string]any{"items": items}
		studentCache.Set(cacheKey, toolResultItem{result: res, data: structured}, studentQueryTTL)
		return res, structured, nil
	}
}

func StudentExamHandler(
	svc *service.StudentService,
) func(context.Context, *mcp.CallToolRequest, StudentQueryByIDArgs) (*mcp.CallToolResult, any, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest, args StudentQueryByIDArgs) (*mcp.CallToolResult, any, error) {
		logger.Toolf("student_exam", "参数: %+v", args)

		cacheKey := "student:exam:" + args.Query
		if val, ok := studentCache.Get(cacheKey); ok {
			cached := val.(toolResultItem)
			logger.Infof("[Cache] student_exam hit cache: id=%s", args.Query)
			return cached.result, cached.data, nil
		}

		items, err := svc.SearchExam(ctx, args.Query)
		if err != nil {
			return &mcp.CallToolResult{
				Content: []mcp.Content{
					&mcp.TextContent{Text: fmt.Sprintf("student exam failed: %v", err)},
				},
				IsError: true,
			}, nil, nil
		}

		data, _ := json.MarshalIndent(items, "", "  ")
		res := &mcp.CallToolResult{
			Content: []mcp.Content{
				&mcp.TextContent{Text: string(data)},
			},
		}
		structured := map[string]any{"items": items}
		studentCache.Set(cacheKey, toolResultItem{result: res, data: structured}, studentQueryTTL)
		return res, structured, nil
	}
}

func StudentGetHandler(
	svc *service.StudentService,
) func(context.Context, *mcp.CallToolRequest, StudentGetArgs) (*mcp.CallToolResult, any, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest, args StudentGetArgs) (*mcp.CallToolResult, any, error) {
		logger.Toolf("student_get", "参数: %+v", args)

		cacheKey := "student:get:" + args.ID
		if val, ok := studentCache.Get(cacheKey); ok {
			cached := val.(toolResultItem)
			logger.Infof("[Cache] student_get hit cache: id=%s", args.ID)
			return cached.result, cached.data, nil
		}

		item, err := svc.Get(ctx, args.ID)
		if err != nil {
			return &mcp.CallToolResult{
				Content: []mcp.Content{
					&mcp.TextContent{Text: fmt.Sprintf("student get failed: %v", err)},
				},
				IsError: true,
			}, nil, nil
		}

		data, _ := json.MarshalIndent(item, "", "  ")
		res := &mcp.CallToolResult{
			Content: []mcp.Content{
				&mcp.TextContent{Text: string(data)},
			},
		}

		studentCache.Set(cacheKey, toolResultItem{result: res, data: item}, 5*time.Minute)
		return res, item, nil
	}
}
