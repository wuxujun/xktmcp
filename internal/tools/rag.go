package tools

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/wuxujun/xktmcp/internal/logger"
	"github.com/wuxujun/xktmcp/internal/service"
)

type RagSearchArgs struct {
	CommonArgs
	Query          string  `json:"query"`
	TopK           int     `json:"top_k"`
	MinScore       float64 `json:"min_score"`
	Rewrite        bool    `json:"rewrite"`
	IncludeSources bool    `json:"include_sources"`
	IncludeChunks  bool    `json:"include_chunks"`
}

type SearchStrategy struct {
	Rewritten bool    `json:"rewritten"`
	TopK      int     `json:"top_k"`
	MinScore  float64 `json:"min_score"`
}

type SourceItem struct {
	SourceID string  `json:"source_id"`
	Title    string  `json:"title"`
	URL      string  `json:"url"`
	Score    float32 `json:"score"`
}

type ChunkItem struct {
	ChunkID string  `json:"chunk_id"`
	Title   string  `json:"title"`
	URL     string  `json:"url"`
	Score   float32 `json:"score"`
	Content string  `json:"content"`
}

type MetaInfo struct {
	HitCount  int   `json:"hit_count"`
	LatencyMS int64 `json:"latency_ms"`
	Cached    bool  `json:"cached"`
}

type RagSearchResponse struct {
	OK             bool           `json:"ok"`
	Query          string         `json:"query"`
	MainQuery      string         `json:"main_query"`
	SearchStrategy SearchStrategy `json:"search_strategy"`
	Context        string         `json:"context"`
	Sources        []SourceItem   `json:"sources"`
	Chunks         []ChunkItem    `json:"chunks"`
	Meta           MetaInfo       `json:"meta"`
}

func RagSearchTool() *mcp.Tool {
	return &mcp.Tool{
		Name:        "rag_search",
		Description: `用于企业知识库检索。输入用户问题，返回经过查询改写、检索、筛选和整理后的上下文与来源信息。当问题涉及知识库事实时，必须优先调用此工具，再基于返回的 context 和 sources 回答。`,
	}
}

func rewriteQuery(query string) string {
	q := query
	if strings.HasSuffix(q, "怎么处理") {
		q = strings.TrimSuffix(q, "怎么处理") + "的处理规则"
	} else if strings.HasSuffix(q, "如何处理") {
		q = strings.TrimSuffix(q, "如何处理") + "的处理规则"
	}
	q = strings.ReplaceAll(q, "后的的", "后的")
	q = strings.ReplaceAll(q, "后考勤", "后的考勤")
	return q
}

func RagSearchHandler(
	svc *service.RagService,
) func(context.Context, *mcp.CallToolRequest, RagSearchArgs) (*mcp.CallToolResult, any, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest, args RagSearchArgs) (*mcp.CallToolResult, any, error) {
		logger.Toolf("rag_search", "参数: %+v", args)

		startTime := time.Now()
		items, err := svc.RagSearch(ctx, args.UserID, args.Query)
		latency := time.Since(startTime).Milliseconds()

		if err != nil {
			return &mcp.CallToolResult{
				Content: []mcp.Content{
					&mcp.TextContent{Text: fmt.Sprintf("rag search failed: %v", err)},
				},
				IsError: true,
			}, nil, nil
		}

		// Set default values for top_k and min_score if they are 0
		topK := args.TopK
		if topK == 0 {
			topK = 5
		}
		minScore := args.MinScore
		if minScore == 0 {
			minScore = 0.2
		}

		mainQuery := args.Query
		if args.Rewrite {
			mainQuery = rewriteQuery(args.Query)
		}

		var contextParts []string
		for i, item := range items {
			part := fmt.Sprintf("## 片段%d\n标题: %s\n内容: %s", i+1, item.Title, item.Content)
			contextParts = append(contextParts, part)
		}
		contextStr := strings.Join(contextParts, "\n\n")

		var sources []SourceItem
		seenURLs := make(map[string]bool)
		for _, item := range items {
			urlKey := item.Url
			if urlKey == "" {
				urlKey = item.Title
			}
			if !seenURLs[urlKey] {
				seenURLs[urlKey] = true
				sourceID := fmt.Sprintf("src_%d", len(sources)+1)
				sources = append(sources, SourceItem{
					SourceID: sourceID,
					Title:    item.Title,
					URL:      item.Url,
					Score:    item.Score,
				})
			}
		}

		var chunks []ChunkItem
		for i, item := range items {
			chunks = append(chunks, ChunkItem{
				ChunkID: fmt.Sprintf("chunk_%d", i+1),
				Title:   item.Title,
				URL:     item.Url,
				Score:   item.Score,
				Content: item.Content,
			})
		}

		resp := RagSearchResponse{
			OK:        true,
			Query:     args.Query,
			MainQuery: mainQuery,
			SearchStrategy: SearchStrategy{
				Rewritten: args.Rewrite,
				TopK:      topK,
				MinScore:  minScore,
			},
			Context: contextStr,
			Sources: sources,
			Chunks:  chunks,
			Meta: MetaInfo{
				HitCount:  len(items),
				LatencyMS: latency,
				Cached:    false,
			},
		}

		return &mcp.CallToolResult{
			Content: []mcp.Content{
				&mcp.TextContent{Text: contextStr},
			},
		}, resp, nil
	}
}
