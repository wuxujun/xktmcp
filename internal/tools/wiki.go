package tools

import (
	"context"
	"fmt"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/wuxujun/xktmcp/internal/logger"
	"github.com/wuxujun/xktmcp/internal/metrics"
	"github.com/wuxujun/xktmcp/internal/model"
	"github.com/wuxujun/xktmcp/internal/pii"
	"github.com/wuxujun/xktmcp/internal/service"
	"github.com/wuxujun/xktmcp/internal/trace"
	wikibackend "github.com/wuxujun/xktmcp/internal/wiki"
)

var wikiCache = sharedCache

const (
	wikiSearchTTL = 2 * time.Minute
	wikiPageTTL   = 5 * time.Minute
	wikiTreeTTL   = 10 * time.Minute
)

// --- Args 定义 ---

type WikiSearchArgs struct {
	CommonArgs
	Query    string `json:"query" jsonschema:"检索知识库词条或文档的关键词"`
	Category string `json:"category,omitempty" jsonschema:"词条所属分类（可选）"`
	TopK     int    `json:"top_k,omitempty" jsonschema:"返回的最优相似词条数量，默认为 5，取值范围 1-20"`
}

func (a WikiSearchArgs) AuditSubject() string { return a.Query }

type WikiGetPageArgs struct {
	CommonArgs
	PageID string `json:"page_id,omitempty" jsonschema:"Wiki 词条的唯一 ID (page_id)"`
	Title  string `json:"title,omitempty" jsonschema:"Wiki 词条的精确或主要标题 (若无 page_id 时使用)"`
}

func (a WikiGetPageArgs) AuditSubject() string {
	if a.PageID != "" {
		return a.PageID
	}
	return a.Title
}

type WikiListTreeArgs struct {
	CommonArgs
	ParentID string `json:"parent_id,omitempty" jsonschema:"父节点/父分类 ID，留空表示获取根节点"`
	Depth    int    `json:"depth,omitempty" jsonschema:"遍历树的最大深度，默认为 3，取值范围 1-10"`
}

func (a WikiListTreeArgs) AuditSubject() string { return a.ParentID }

type WikiUpsertPageArgs struct {
	CommonArgs
	Title    string `json:"title" jsonschema:"Wiki 词条标题"`
	Content  string `json:"content" jsonschema:"Wiki 词条正文内容，采用 Markdown 格式"`
	Category string `json:"category,omitempty" jsonschema:"词条所属分类"`
	Summary  string `json:"summary,omitempty" jsonschema:"词条简要概述（可选）"`
	Mode     string `json:"mode,omitempty" jsonschema:"写入模式：create（新建）、update（覆盖更新）或 append（追加内容），默认为 create"`
}

func (a WikiUpsertPageArgs) AuditSubject() string { return a.Title }

type WikiGetBacklinksArgs struct {
	CommonArgs
	PageID string `json:"page_id" jsonschema:"目标 Wiki 词条的唯一 ID (page_id)"`
}

func (a WikiGetBacklinksArgs) AuditSubject() string { return a.PageID }

type WikiSearchResponse struct {
	Items []model.WikiSearchResult `json:"items"`
}

type WikiBacklinksResponse struct {
	Items []model.WikiBacklink `json:"items"`
}

// --- Tool 声明 ---

func WikiSearchTool() *mcp.Tool {
	return &mcp.Tool{
		Name:         "wiki_search",
		Description:  `知识库 Wiki 检索工具。根据关键词与分类搜索词条概览。当需要查找某概念、规范、业务说明或产品维基词条时调用。`,
		InputSchema:  publicSchema[WikiSearchArgs](envelopeFields),
		OutputSchema: outputSchema[WikiSearchResponse](),
	}
}

func WikiGetPageTool() *mcp.Tool {
	return &mcp.Tool{
		Name:         "wiki_get_page",
		Description:  `获取指定 Wiki 词条或文档的完整 Markdown 正文与元信息。可通过 page_id 或 title 获取。`,
		InputSchema:  publicSchema[WikiGetPageArgs](envelopeFields),
		OutputSchema: outputSchema[model.WikiPage](),
	}
}

func WikiListTreeTool() *mcp.Tool {
	return &mcp.Tool{
		Name:        "wiki_list_tree",
		Description: `获取知识库 Wiki 的层级目录大纲与分类树。用于梳理知识框架、探索上下级分类或浏览词条结构。`,
		InputSchema: publicSchema[WikiListTreeArgs](envelopeFields),
		OutputSchema: &jsonschema.Schema{
			Type: "object",
			Properties: map[string]*jsonschema.Schema{
				"items": {
					Type: "array",
					Items: &jsonschema.Schema{
						Type: "object",
						Properties: map[string]*jsonschema.Schema{
							"id":           {Type: "string", Description: "节点唯一标识"},
							"title":        {Type: "string", Description: "节点标题/分类名"},
							"category":     {Type: "string", Description: "分类"},
							"has_children": {Type: "boolean", Description: "是否有子节点"},
							"children":     {Type: "array", Description: "子节点列表"},
						},
						Required: []string{"id", "title", "has_children"},
					},
				},
			},
			Required: []string{"items"},
		},
	}
}

func WikiUpsertPageTool() *mcp.Tool {
	return &mcp.Tool{
		Name:         "wiki_upsert_page",
		Description:  `创建、覆盖更新或追加 Wiki 词条内容。当需要沉淀新知识、补充已有文档或修正词条时调用。`,
		InputSchema:  publicSchema[WikiUpsertPageArgs](envelopeFields),
		OutputSchema: outputSchema[model.WikiUpsertResult](),
	}
}

func WikiGetBacklinksTool() *mcp.Tool {
	return &mcp.Tool{
		Name:         "wiki_get_backlinks",
		Description:  `获取引用了指定 Wiki 词条的所有反向关联词条列表。用于探索知识图谱关系与上下游依赖。`,
		InputSchema:  publicSchema[WikiGetBacklinksArgs](envelopeFields),
		OutputSchema: outputSchema[WikiBacklinksResponse](),
	}
}

// --- Handlers ---

func WikiSearchHandler(
	svc *service.WikiService,
	resourceLinkBaseURL string,
) func(context.Context, *mcp.CallToolRequest, WikiSearchArgs) (*mcp.CallToolResult, any, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest, args WikiSearchArgs) (*mcp.CallToolResult, any, error) {
		userID := trace.EffectiveUserID(ctx, args.UserID)
		logger.ToolfCtx(ctx, "wiki_search", "querier=%s subject=%s category=%s top_k=%d", userID, pii.MaskSubject(args.Query), args.Category, args.TopK)

		topK := args.TopK
		if topK <= 0 {
			topK = 5
		}

		cacheKey := fmt.Sprintf("wiki:search:%s:%s:%s:%d:%s", userID, args.Query, args.Category, topK, resourceLinkBaseURL)
		if val, ok := wikiCache.Get(cacheKey); ok {
			cached := val.(toolResultItem)
			logger.InfofCtx(ctx, "[Cache] wiki_search hit cache: query=%s", args.Query)
			metrics.ObserveCacheAccess("wiki_search", true)
			return cached.result, cached.data, nil
		}
		metrics.ObserveCacheAccess("wiki_search", false)

		items, err := svc.Search(ctx, userID, args.Query, args.Category, topK)
		if err != nil {
			return &mcp.CallToolResult{
				Content: []mcp.Content{
					&mcp.TextContent{Text: fmt.Sprintf("wiki search failed: %v", err)},
				},
				IsError: true,
			}, nil, nil
		}

		text, redacted := pii.RedactJSON(items)
		content := []mcp.Content{
			&mcp.TextContent{Text: text},
		}
		for _, item := range items {
			resourceURI, err := wikibackend.PageResourceLinkURI(item.PageID, resourceLinkBaseURL)
			if err != nil {
				continue
			}
			content = append(content, &mcp.ResourceLink{
				URI:         resourceURI,
				Name:        item.PageID,
				Title:       pii.Redact(item.Title),
				Description: pii.Redact(item.Summary),
				MIMEType:    "text/markdown",
			})
		}
		res := &mcp.CallToolResult{
			Content: content,
		}
		structured := map[string]any{"items": redacted}
		wikiCache.Set(cacheKey, toolResultItem{result: res, data: structured}, wikiSearchTTL)
		return res, structured, nil
	}
}

func WikiGetPageHandler(
	svc *service.WikiService,
) func(context.Context, *mcp.CallToolRequest, WikiGetPageArgs) (*mcp.CallToolResult, any, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest, args WikiGetPageArgs) (*mcp.CallToolResult, any, error) {
		userID := trace.EffectiveUserID(ctx, args.UserID)
		logger.ToolfCtx(ctx, "wiki_get_page", "querier=%s page_id=%s title=%s", userID, pii.MaskSubject(args.PageID), pii.MaskSubject(args.Title))

		cacheKey := fmt.Sprintf("wiki:page:%s:%s:%s", userID, args.PageID, args.Title)
		if val, ok := wikiCache.Get(cacheKey); ok {
			cached := val.(toolResultItem)
			logger.InfofCtx(ctx, "[Cache] wiki_get_page hit cache: page_id=%s title=%s", args.PageID, args.Title)
			metrics.ObserveCacheAccess("wiki_get_page", true)
			return cached.result, cached.data, nil
		}
		metrics.ObserveCacheAccess("wiki_get_page", false)

		page, err := svc.GetPage(ctx, userID, args.PageID, args.Title)
		if err != nil {
			return &mcp.CallToolResult{
				Content: []mcp.Content{
					&mcp.TextContent{Text: fmt.Sprintf("get wiki page failed: %v", err)},
				},
				IsError: true,
			}, nil, nil
		}

		text, redacted := pii.RedactJSON(page)
		res := &mcp.CallToolResult{
			Content: []mcp.Content{
				&mcp.TextContent{Text: text},
			},
		}
		wikiCache.Set(cacheKey, toolResultItem{result: res, data: redacted}, wikiPageTTL)
		return res, redacted, nil
	}
}

func WikiListTreeHandler(
	svc *service.WikiService,
) func(context.Context, *mcp.CallToolRequest, WikiListTreeArgs) (*mcp.CallToolResult, any, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest, args WikiListTreeArgs) (*mcp.CallToolResult, any, error) {
		userID := trace.EffectiveUserID(ctx, args.UserID)
		logger.ToolfCtx(ctx, "wiki_list_tree", "querier=%s parent_id=%s depth=%d", userID, args.ParentID, args.Depth)

		depth := args.Depth
		if depth <= 0 {
			depth = 3
		}

		cacheKey := fmt.Sprintf("wiki:tree:%s:%s:%d", userID, args.ParentID, depth)
		if val, ok := wikiCache.Get(cacheKey); ok {
			cached := val.(toolResultItem)
			logger.InfofCtx(ctx, "[Cache] wiki_list_tree hit cache: parent_id=%s", args.ParentID)
			metrics.ObserveCacheAccess("wiki_list_tree", true)
			return cached.result, cached.data, nil
		}
		metrics.ObserveCacheAccess("wiki_list_tree", false)

		nodes, err := svc.ListTree(ctx, userID, args.ParentID, depth)
		if err != nil {
			return &mcp.CallToolResult{
				Content: []mcp.Content{
					&mcp.TextContent{Text: fmt.Sprintf("list wiki tree failed: %v", err)},
				},
				IsError: true,
			}, nil, nil
		}

		text, redacted := pii.RedactJSON(nodes)
		res := &mcp.CallToolResult{
			Content: []mcp.Content{
				&mcp.TextContent{Text: text},
			},
		}
		structured := map[string]any{"items": redacted}
		wikiCache.Set(cacheKey, toolResultItem{result: res, data: structured}, wikiTreeTTL)
		return res, structured, nil
	}
}

func WikiUpsertPageHandler(
	svc *service.WikiService,
) func(context.Context, *mcp.CallToolRequest, WikiUpsertPageArgs) (*mcp.CallToolResult, any, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest, args WikiUpsertPageArgs) (*mcp.CallToolResult, any, error) {
		userID := trace.EffectiveUserID(ctx, args.UserID)
		logger.ToolfCtx(ctx, "wiki_upsert_page", "querier=%s title=%s mode=%s category=%s", userID, pii.MaskSubject(args.Title), args.Mode, args.Category)

		result, err := svc.UpsertPage(ctx, userID, args.Title, args.Content, args.Category, args.Summary, args.Mode)
		if err != nil {
			return &mcp.CallToolResult{
				Content: []mcp.Content{
					&mcp.TextContent{Text: fmt.Sprintf("upsert wiki page failed: %v", err)},
				},
				IsError: true,
			}, nil, nil
		}

		invalidateWikiCache(userID)

		text, redacted := pii.RedactJSON(result)
		res := &mcp.CallToolResult{
			Content: []mcp.Content{
				&mcp.TextContent{Text: text},
			},
		}
		return res, redacted, nil
	}
}

func invalidateWikiCache(userID string) {
	if userID == "" {
		wikiCache.DeletePrefix("wiki:")
		return
	}

	for _, operation := range []string{"search", "page", "tree", "backlinks"} {
		wikiCache.DeletePrefix(fmt.Sprintf("wiki:%s:%s:", operation, userID))
	}
}

func WikiGetBacklinksHandler(
	svc *service.WikiService,
) func(context.Context, *mcp.CallToolRequest, WikiGetBacklinksArgs) (*mcp.CallToolResult, any, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest, args WikiGetBacklinksArgs) (*mcp.CallToolResult, any, error) {
		userID := trace.EffectiveUserID(ctx, args.UserID)
		logger.ToolfCtx(ctx, "wiki_get_backlinks", "querier=%s page_id=%s", userID, pii.MaskSubject(args.PageID))

		links, err := svc.GetBacklinks(ctx, userID, args.PageID)
		if err != nil {
			return &mcp.CallToolResult{
				Content: []mcp.Content{
					&mcp.TextContent{Text: fmt.Sprintf("get wiki backlinks failed: %v", err)},
				},
				IsError: true,
			}, nil, nil
		}

		text, redacted := pii.RedactJSON(links)
		res := &mcp.CallToolResult{
			Content: []mcp.Content{
				&mcp.TextContent{Text: text},
			},
		}
		return res, map[string]any{"items": redacted}, nil
	}
}
