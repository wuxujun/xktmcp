package tools

import (
	"context"
	"errors"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/wuxujun/xktmcp/internal/logger"
	"github.com/wuxujun/xktmcp/internal/model"
	"github.com/wuxujun/xktmcp/internal/pii"
	"github.com/wuxujun/xktmcp/internal/service"
)

type FileInfoArgs struct {
	CommonArgs
	Path string `json:"path"`
}
type FilePreviewArgs struct {
	CommonArgs
	Path      string `json:"path"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
}

func (a FileInfoArgs) AuditSubject() string    { return a.Path }
func (a FilePreviewArgs) AuditSubject() string { return a.Path }

type FileSearchArgs struct {
	CommonArgs
	Query    string `json:"query" jsonschema:"文件标题、文件名或正文中的关键词，最长 256 个字符；不区分大小写的连续文本匹配"`
	SearchIn string `json:"search_in,omitempty" jsonschema:"搜索范围：all（标题及正文，默认）、title（标题或文件名）、content（正文）"`
	Limit    int    `json:"limit,omitempty" jsonschema:"最多返回的文件数量，默认 20，最大 100"`
}

func (a FileSearchArgs) AuditSubject() string { return a.Query }

type FileSearchResponse struct {
	Items []model.FileSearchResult `json:"items"`
}

func FileSearchTool() *mcp.Tool {
	schema := publicSchema[FileSearchArgs](envelopeFields)
	schema.Properties["search_in"].Enum = []any{"all", "title", "content"}
	return &mcp.Tool{
		Name:        "file_search",
		Description: "搜索服务器预先配置目录中的文件。支持文件名、Markdown 一级标题及 UTF-8 文本正文搜索，返回相对路径、标题、命中摘要和命中字段。标题命中优先。不会读取任意指定路径。",
		InputSchema: schema, OutputSchema: outputSchema[FileSearchResponse](),
	}
}

func FileSearchHandler(svc *service.FileService) func(context.Context, *mcp.CallToolRequest, FileSearchArgs) (*mcp.CallToolResult, any, error) {
	return func(ctx context.Context, _ *mcp.CallToolRequest, args FileSearchArgs) (*mcp.CallToolResult, any, error) {
		items, err := svc.Search(ctx, args.Query, args.SearchIn, args.Limit)
		if err != nil {
			message := "file search failed; check server logs for details"
			switch {
			case errors.Is(err, service.ErrInvalidQuery), errors.Is(err, service.ErrInvalidFileSearchScope), errors.Is(err, service.ErrFileSearchQueryTooLong):
				message = err.Error()
			case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
				message = "file search canceled or timed out"
			default:
				logger.ErrorfCtx(ctx, "file_search failed: %v", err)
			}
			return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: message}}}, nil, nil
		}
		text, redacted := pii.RedactJSON(items)
		result := &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}
		return result, map[string]any{"items": redacted}, nil
	}
}

func FileInfoTool() *mcp.Tool {
	return &mcp.Tool{Name: "file_get_info", Description: "获取搜索根目录内文件的元数据。", InputSchema: publicSchema[FileInfoArgs](envelopeFields), OutputSchema: outputSchema[model.FileInfo]()}
}
func FilePreviewTool() *mcp.Tool {
	return &mcp.Tool{Name: "file_read_preview", Description: "按行号区间预览 UTF-8 文本文件。", InputSchema: publicSchema[FilePreviewArgs](envelopeFields), OutputSchema: outputSchema[model.FilePreview]()}
}

func genericFileResult(ctx context.Context, value any, err error) (*mcp.CallToolResult, any, error) {
	if err != nil {
		logger.ErrorfCtx(ctx, "file tool failed: %v", err)
		return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}}}, nil, nil
	}
	text, redacted := pii.RedactJSON(value)
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}, redacted, nil
}
func FileInfoHandler(svc *service.FileService) func(context.Context, *mcp.CallToolRequest, FileInfoArgs) (*mcp.CallToolResult, any, error) {
	return func(ctx context.Context, _ *mcp.CallToolRequest, a FileInfoArgs) (*mcp.CallToolResult, any, error) {
		v, e := svc.GetFileInfo(ctx, a.Path)
		return genericFileResult(ctx, v, e)
	}
}
func FilePreviewHandler(svc *service.FileService) func(context.Context, *mcp.CallToolRequest, FilePreviewArgs) (*mcp.CallToolResult, any, error) {
	return func(ctx context.Context, _ *mcp.CallToolRequest, a FilePreviewArgs) (*mcp.CallToolResult, any, error) {
		v, e := svc.ReadFilePreview(ctx, a.Path, a.StartLine, a.EndLine)
		return genericFileResult(ctx, v, e)
	}
}
