package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
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
		InputSchema: publicSchema[RagSearchArgs](envelopeFields),
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

// semanticRewriteEnabled 控制 rewrite=true 时是否尝试 MCP sampling 语义改写。
// 默认开启;设环境变量 RAG_SEMANTIC_REWRITE=false/0/no/off 可全局关闭(总是走本地规则改写)。
//
// 使用 sync.Once 延迟读取:.env 由 main() 的 godotenv.Load() 装载,而包级 var 初始化
// 早于 main(),若在包初始化时读 env 会漏掉 .env 的值。延迟到首次调用即可避开此序问题。
var (
	semanticRewriteOnce sync.Once
	semanticRewriteOn   bool
)

func semanticRewriteEnabled() bool {
	semanticRewriteOnce.Do(func() {
		v := strings.ToLower(strings.TrimSpace(os.Getenv("RAG_SEMANTIC_REWRITE")))
		semanticRewriteOn = v != "false" && v != "0" && v != "no" && v != "off"
		if !semanticRewriteOn {
			logger.Infof("[RAG] 语义改写已通过 RAG_SEMANTIC_REWRITE 关闭,rewrite=true 时将使用本地规则改写")
		}
	})
	return semanticRewriteOn
}

// samplingUnsupported 记录"已确认连接的客户端不支持 MCP sampling"。
// n8n 等多数编排型客户端未实现 sampling/createMessage,首次调用会返回 Method not found;
// 记住该结果后,后续 rewrite=true 直接走本地规则改写,避免每次都发一次注定失败的往返
// 并刷一条错误日志。注意:这是进程级标志——若同一进程服务多个客户端,其中一个不支持
// 即对全部生效(代价只是退化为本地改写,功能不受损)。
var samplingUnsupported atomic.Bool

// isMethodNotFound 判断错误是否为 JSON-RPC "Method not found"(客户端未实现该方法)。
func isMethodNotFound(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "method not found")
}

func rewriteQuerySemantic(ctx context.Context, session *mcp.ServerSession, query string) string {
	if session == nil || !semanticRewriteEnabled() || samplingUnsupported.Load() {
		return rewriteQuery(query)
	}

	systemPrompt := `你是一个专业的搜索查询改写助手。你的任务是将用户输入的提问改写为最适合在企业知识库中进行检索（RAG）的精准查询词或短语。
规则：
1. 保持原意：不要引入多余的概念，也不要回答问题。
2. 转换为检索词：将类似"怎么处理"、"如何处理"等口语化提问转换为"的处理规则"、"的流程"或具体的检索关键词。
3. 紧凑精炼：直接返回改写后的查询语句，不要包含任何前导词或解释（如：改写为：...）。`

	userPrompt := fmt.Sprintf("请改写以下查询：\n%s", query)

	params := &mcp.CreateMessageParams{
		SystemPrompt: systemPrompt,
		MaxTokens:    100,
		Messages: []*mcp.SamplingMessage{
			{
				Role:    mcp.Role("user"),
				Content: &mcp.TextContent{Text: userPrompt},
			},
		},
		Temperature: 0.1,
	}

	res, err := session.CreateMessage(ctx, params)
	if err != nil {
		if isMethodNotFound(err) {
			// 客户端没实现 sampling:属预期内的能力缺失,只在首次记一条 INFO,
			// 并置标志使后续调用直接走本地改写。
			if samplingUnsupported.CompareAndSwap(false, true) {
				logger.Infof("[RAG] 客户端不支持 MCP sampling(Method not found),本进程后续查询改写将直接使用本地规则")
			}
		} else {
			logger.Errorf("[RAG] LLM semantic rewrite query failed: %v, falling back to local rewrite", err)
		}
		return rewriteQuery(query)
	}

	if res.Content == nil {
		return rewriteQuery(query)
	}

	if textContent, ok := res.Content.(*mcp.TextContent); ok {
		rewritten := strings.TrimSpace(textContent.Text)
		if rewritten != "" {
			logger.Infof("[RAG] LLM semantic rewrite success: %s -> %s", query, rewritten)
			return rewritten
		}
	}

	// Support fallback by marshaling Content to JSON and unmarshaling into a map
	if data, err := json.Marshal(res.Content); err == nil {
		var rawMap map[string]interface{}
		if err := json.Unmarshal(data, &rawMap); err == nil {
			if textVal, found := rawMap["text"].(string); found && textVal != "" {
				rewritten := strings.TrimSpace(textVal)
				logger.Infof("[RAG] LLM semantic rewrite success (parsed via JSON): %s -> %s", query, rewritten)
				return rewritten
			}
		}
	}

	logger.Errorf("[RAG] LLM semantic rewrite returned unexpected content type: %T, falling back", res.Content)
	return rewriteQuery(query)
}

func RagSearchHandler(
	svc *service.RagService,
) func(context.Context, *mcp.CallToolRequest, RagSearchArgs) (*mcp.CallToolResult, any, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest, args RagSearchArgs) (*mcp.CallToolResult, any, error) {
		logger.Toolf("rag_search", "参数: %+v", args)

		mainQuery := args.Query
		if args.Rewrite {
			mainQuery = rewriteQuerySemantic(ctx, req.Session, args.Query)
		}

		startTime := time.Now()
		items, err := svc.RagSearch(ctx, args.UserID, mainQuery)
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
