package server

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/wuxujun/xktmcp/internal/client"
	"github.com/wuxujun/xktmcp/internal/logger"
	"github.com/wuxujun/xktmcp/internal/metrics"
	"github.com/wuxujun/xktmcp/internal/pii"
	"github.com/wuxujun/xktmcp/internal/prompts"
	"github.com/wuxujun/xktmcp/internal/service"
	"github.com/wuxujun/xktmcp/internal/tools"
	"github.com/wuxujun/xktmcp/internal/trace"
	wikibackend "github.com/wuxujun/xktmcp/internal/wiki"
)

// auditable 由内嵌 CommonArgs 的工具 Args 满足:
//   - CorrelationID/Querier 经 CommonArgs 提升;
//   - AuditSubject 由各 Args 自身实现(返回被查询的 query/id)。
//
// 用于从入参里取 trace 关联 id、查询者与被查主体。
type auditable interface {
	CorrelationID() string
	Querier() string
	AuditSubject() string
}

// RegisterAll 装配依赖并注册所有 MCP 工具(均带统一埋点:trace id + 指标 + 摘要日志)。
func RegisterAll(s *mcp.Server, wikiConfigPaths ...string) error {
	enabledTools, err := parseEnabledTools(os.Getenv("MCP_ENABLED_TOOLS"))
	if err != nil {
		return err
	}
	prompts.RegisterAll(s, enabledTools)
	breakerSet, err := client.LoadCircuitBreakerSetFromEnv()
	if err != nil {
		return err
	}

	wikiConfigPath := "config/wiki.json"
	if len(wikiConfigPaths) > 0 && wikiConfigPaths[0] != "" {
		wikiConfigPath = wikiConfigPaths[0]
	}
	wikiConfig, err := wikibackend.LoadConfig(wikiConfigPath)
	if err != nil {
		return err
	}

	upstreamToolsEnabled := toolEnabled(enabledTools, "student_search") ||
		toolEnabled(enabledTools, "student_order") ||
		toolEnabled(enabledTools, "student_exam") ||
		toolEnabled(enabledTools, "student_get") ||
		toolEnabled(enabledTools, "rag_search") ||
		toolEnabled(enabledTools, "staff_search")
	baseCfg := client.Config{}
	if wikiConfig.Mode != wikibackend.ModeLocal || upstreamToolsEnabled {
		baseCfg, err = client.LoadConfigFromEnv()
		if err != nil {
			return err
		}
	}

	studentAPI := client.NewStudentAPI(baseCfg, breakerSet.Student)
	studentSvc := service.NewStudentService(studentAPI)
	if toolEnabled(enabledTools, "student_search") {
		addTool(s, tools.StudentSearchTool(), tools.StudentSearchHandler(studentSvc))
	}
	if toolEnabled(enabledTools, "student_order") {
		addTool(s, tools.StudentOrderTool(), tools.StudentOrderHandler(studentSvc))
	}
	if toolEnabled(enabledTools, "student_exam") {
		addTool(s, tools.StudentExamTool(), tools.StudentExamHandler(studentSvc))
	}
	if toolEnabled(enabledTools, "student_get") {
		addTool(s, tools.StudentGetTool(), tools.StudentGetHandler(studentSvc))
	}

	ragAPI := client.NewRagAPI(baseCfg, breakerSet.RAG)
	ragSvc := service.NewRagService(ragAPI)
	if toolEnabled(enabledTools, "rag_search") {
		addTool(s, tools.RagSearchTool(), tools.RagSearchHandler(ragSvc))
	}

	staffAPI := client.NewStaffAPI(baseCfg, breakerSet.Staff)
	staffSvc := service.NewStaffService(staffAPI)
	if toolEnabled(enabledTools, "staff_search") {
		addTool(s, tools.StaffSearchTool(), tools.StaffSearchHandler(staffSvc))
	}

	return registerWikiTools(s, baseCfg, wikiConfig, enabledTools, breakerSet.Wiki)
}

func registerWikiTools(s *mcp.Server, baseCfg client.Config, wikiConfig wikibackend.Config, enabledTools map[string]bool, breaker *client.CircuitBreaker) error {
	wikiAPI := client.NewWikiAPI(baseCfg, breaker)
	var wikiBackend service.WikiBackend = wikiAPI
	var localRouter *wikibackend.LocalRouter
	if wikiConfig.Mode == wikibackend.ModeLocal {
		var err error
		localRouter, err = wikibackend.NewLocalRouter(wikiConfig.Local)
		if err != nil {
			return err
		}
		wikiBackend = localRouter
		logger.Infof("Wiki 后端: local root=%s content_dirs=%v write_dir=%s configured_users=%d strict_user_mapping=%t indexed_documents=%d",
			wikiConfig.Local.Root, wikiConfig.Local.ContentDirs, wikiConfig.Local.WriteDir, localRouter.UserCount(),
			wikiConfig.Local.RequireUserMapping, localRouter.DocumentCount())
	} else {
		logger.Infof("Wiki 后端: http base_url=%s", baseCfg.BaseURL)
	}
	wikiSvc := service.NewWikiService(wikiAPI, wikiBackend)
	if toolEnabled(enabledTools, "wiki_search") {
		addTool(s, tools.WikiSearchTool(), tools.WikiSearchHandler(wikiSvc, wikiConfig.Resources.LinkBaseURL))
	}
	if toolEnabled(enabledTools, "wiki_get_page") {
		addTool(s, tools.WikiGetPageTool(), tools.WikiGetPageHandler(wikiSvc))
	}
	if toolEnabled(enabledTools, "wiki_list_tree") {
		addTool(s, tools.WikiListTreeTool(), tools.WikiListTreeHandler(wikiSvc))
	}
	if toolEnabled(enabledTools, "wiki_upsert_page") {
		addTool(s, tools.WikiUpsertPageTool(), tools.WikiUpsertPageHandler(wikiSvc))
	}
	if toolEnabled(enabledTools, "wiki_get_backlinks") {
		addTool(s, tools.WikiGetBacklinksTool(), tools.WikiGetBacklinksHandler(wikiSvc))
	}
	if localRouter != nil && wikiConfig.Resources.Enabled {
		registerWikiResources(s, localRouter, wikiConfig.Resources)
	}

	return nil
}

var knownToolNames = map[string]struct{}{
	"student_search": {}, "student_order": {}, "student_exam": {}, "student_get": {},
	"rag_search": {}, "staff_search": {}, "wiki_search": {}, "wiki_get_page": {},
	"wiki_list_tree": {}, "wiki_upsert_page": {}, "wiki_get_backlinks": {},
}

func parseEnabledTools(raw string) (map[string]bool, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	set := make(map[string]bool)
	for _, item := range strings.Split(raw, ",") {
		name := strings.TrimSpace(item)
		if name == "" {
			continue
		}
		if _, ok := knownToolNames[name]; !ok {
			return nil, fmt.Errorf("unknown MCP tool %q in MCP_ENABLED_TOOLS", name)
		}
		set[name] = true
	}
	if len(set) == 0 {
		return nil, fmt.Errorf("MCP_ENABLED_TOOLS must contain at least one known tool")
	}
	return set, nil
}

func toolEnabled(enabled map[string]bool, name string) bool {
	return enabled == nil || enabled[name]
}

type toolHandler[In any] func(
	context.Context,
	*mcp.CallToolRequest,
	In,
) (*mcp.CallToolResult, any, error)

// wrapToolHandler 注册工具并包裹一层统一埋点:
//   - 从入参取 n8n 关联 id(或新生成)作为 trace id 注入 context,贯穿后续各层日志;
//   - 校验可信认证主体与显式或路由 userId 的一致性;
//   - 计时并上报 Prometheus 指标(调用量/错误数/耗时);
//   - 写一条结构化【审计日志】:谁(querier)用哪个工具(tool)查了谁(subject,已脱敏)、
//     结果状态与耗时、trace_id——满足「谁查了哪个学员」的合规留痕诉求;
//   - 调用结束打一条带 trace_id 的摘要日志(状态 + 耗时)。
//
// 失败判定:handler 返回 error 或结果 IsError=true 都计为 error。
func wrapToolHandler[In auditable](name string, h toolHandler[In]) toolHandler[In] {
	return func(ctx context.Context, req *mcp.CallToolRequest, in In) (*mcp.CallToolResult, any, error) {
		ctx, _ = trace.EnsureID(ctx, in.CorrelationID())

		querier, identityErr := trace.ResolveUserID(ctx, in.Querier())
		start := time.Now()
		var res *mcp.CallToolResult
		var out any
		var err error
		if identityErr != nil {
			querier = trace.AuthenticatedUserIDFromContext(ctx)
			res = &mcp.CallToolResult{
				Content: []mcp.Content{&mcp.TextContent{Text: "authenticated user identity conflict"}},
				IsError: true,
			}
		} else {
			res, out, err = h(ctx, req, in)
		}
		elapsed := time.Since(start)

		status := metrics.StatusOK
		if err != nil || (res != nil && res.IsError) {
			status = metrics.StatusError
		}
		metrics.ObserveToolCall(name, status, elapsed)

		// 审计留痕:被查主体脱敏后记录(手机号/证件号掩码,标识符部分掩码)。
		logger.AuditCtx(ctx, map[string]any{
			"tool":       name,
			"querier":    querier,
			"subject":    pii.MaskSubject(in.AuditSubject()),
			"status":     status,
			"latency_ms": elapsed.Milliseconds(),
		})

		logger.ToolfCtx(ctx, name, "调用完成 status=%s latency=%dms", status, elapsed.Milliseconds())

		return res, out, err
	}
}

func addTool[In auditable](s *mcp.Server, tool *mcp.Tool, h toolHandler[In]) {
	mcp.AddTool[In, any](s, tool, mcp.ToolHandlerFor[In, any](wrapToolHandler(tool.Name, h)))
}
