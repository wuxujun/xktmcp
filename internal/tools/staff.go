package tools

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/wuxujun/xktmcp/internal/logger"
	"github.com/wuxujun/xktmcp/internal/pii"
	"github.com/wuxujun/xktmcp/internal/service"
)

type StaffSearchArgs struct {
	CommonArgs
	Query string `json:"query" jsonschema:"查询关键字，可以输入员工姓名、工号、教师、校区、院系或课程等模糊关键字"`
}

// AuditSubject 返回被查询主体(供审计记录,会在上层脱敏后落日志)。
func (a StaffSearchArgs) AuditSubject() string { return a.Query }

func StaffSearchTool() *mcp.Tool {
	return &mcp.Tool{
		Name:        "staff_search",
		Description: `企业/教育机构信息查询工具。凡是用户问题涉及员工、userid、教师、校区、学院、部门、学科、课程、专业或它们之间的关系查询，必须优先调用本工具。工具会返回可用于回答的 context 和 sources。模型必须基于返回的 context 与 sources 作答，不得直接猜测；若存在重名、歧义或信息不足，需先澄清或明确说明未查到。`,
		InputSchema: publicSchema[StaffSearchArgs](envelopeFields),
	}
}

func StaffSearchHandler(
	svc *service.StaffService,
) func(context.Context, *mcp.CallToolRequest, StaffSearchArgs) (*mcp.CallToolResult, any, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest, args StaffSearchArgs) (*mcp.CallToolResult, any, error) {
		logger.ToolfCtx(ctx, "staff_search", "querier=%s subject=%s", args.UserID, pii.MaskSubject(args.Query))

		cacheKey := "staff:search:" + args.Query
		if val, ok := sharedCache.Get(cacheKey); ok {
			cached := val.(toolResultItem)
			logger.InfofCtx(ctx, "[Cache] staff_search hit cache: query=%s", args.Query)
			return cached.result, cached.data, nil
		}

		items, err := svc.StaffSearch(ctx, args.UserID, args.Query)
		if err != nil {
			return &mcp.CallToolResult{
				Content: []mcp.Content{
					&mcp.TextContent{Text: fmt.Sprintf("staff search failed: %v", err)},
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
		// 员工/机构信息相对稳定,沿用与 student 查询一致的 60s TTL。
		sharedCache.Set(cacheKey, toolResultItem{result: res, data: structured}, studentQueryTTL)
		return res, structured, nil
	}
}
