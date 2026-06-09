package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/wuxujun/xktmcp/internal/logger"
	"github.com/wuxujun/xktmcp/internal/service"
)

type StaffSearchArgs struct {
	CommonArgs
	Query string `json:"query"`
}

func StaffSearchTool() *mcp.Tool {
	return &mcp.Tool{
		Name:        "staff_search",
		Description: `企业/教育机构信息查询工具。凡是用户问题涉及员工、userid、教师、校区、学院、部门、学科、课程、专业或它们之间的关系查询，必须优先调用本工具。工具会返回可用于回答的 context 和 sources。模型必须基于返回的 context 与 sources 作答，不得直接猜测；若存在重名、歧义或信息不足，需先澄清或明确说明未查到。`,
		InputSchema: publicSchema[RagSearchArgs](envelopeFields),
	}
}

func StaffSearchHandler(
	svc *service.StaffService,
) func(context.Context, *mcp.CallToolRequest, StaffSearchArgs) (*mcp.CallToolResult, any, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest, args StaffSearchArgs) (*mcp.CallToolResult, any, error) {
		logger.Toolf("staff_search", "参数: %+v", args)

		items, err := svc.StaffSearch(ctx, args.UserID, args.Query)
		if err != nil {
			return &mcp.CallToolResult{
				Content: []mcp.Content{
					&mcp.TextContent{Text: fmt.Sprintf("staff search failed: %v", err)},
				},
				IsError: true,
			}, nil, nil
		}

		data, _ := json.MarshalIndent(items, "", "  ")
		return &mcp.CallToolResult{
			Content: []mcp.Content{
				&mcp.TextContent{Text: string(data)},
			},
		}, map[string]any{"items": items}, nil
	}
}
