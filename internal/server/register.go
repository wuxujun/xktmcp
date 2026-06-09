package server

import (
	"context"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/wuxujun/xktmcp/internal/client"
	"github.com/wuxujun/xktmcp/internal/logger"
	"github.com/wuxujun/xktmcp/internal/metrics"
	"github.com/wuxujun/xktmcp/internal/service"
	"github.com/wuxujun/xktmcp/internal/tools"
	"github.com/wuxujun/xktmcp/internal/trace"
)

// correlatable 由内嵌 CommonArgs 的工具 Args 结构体满足(CorrelationID 方法被提升),
// 用于从入参里取 n8n 透传的关联 id 作为 trace id。
type correlatable interface {
	CorrelationID() string
}

// RegisterAll 装配依赖并注册所有 MCP 工具(均带统一埋点:trace id + 指标 + 摘要日志)。
func RegisterAll(s *mcp.Server) error {
	baseCfg, err := client.LoadConfigFromEnv()
	if err != nil {
		return err
	}

	studentAPI := client.NewStudentAPI(baseCfg)
	studentSvc := service.NewStudentService(studentAPI)

	addTool(s, tools.StudentSearchTool(), tools.StudentSearchHandler(studentSvc))
	addTool(s, tools.StudentOrderTool(), tools.StudentOrderHandler(studentSvc))
	addTool(s, tools.StudentExamTool(), tools.StudentExamHandler(studentSvc))
	addTool(s, tools.StudentGetTool(), tools.StudentGetHandler(studentSvc))

	//Rag搜索
	ragAPI := client.NewRagAPI(baseCfg)
	ragSvc := service.NewRagService(ragAPI)
	addTool(s, tools.RagSearchTool(), tools.RagSearchHandler(ragSvc))

	//Staff搜索
	staffAPI := client.NewStaffAPI(baseCfg)
	staffSvc := service.NewStaffService(staffAPI)
	addTool(s, tools.StaffSearchTool(), tools.StaffSearchHandler(staffSvc))

	return nil
}

// addTool 注册工具并包裹一层统一埋点:
//   - 从入参取 n8n 关联 id(或新生成)作为 trace id 注入 context,贯穿后续各层日志;
//   - 计时并上报 Prometheus 指标(调用量/错误数/耗时);
//   - 调用结束打一条带 trace_id 的摘要日志(状态 + 耗时)。
//
// 失败判定:handler 返回 error 或结果 IsError=true 都计为 error。
func addTool[In correlatable](
	s *mcp.Server,
	tool *mcp.Tool,
	h func(context.Context, *mcp.CallToolRequest, In) (*mcp.CallToolResult, any, error),
) {
	name := tool.Name
	wrapped := func(ctx context.Context, req *mcp.CallToolRequest, in In) (*mcp.CallToolResult, any, error) {
		ctx, _ = trace.EnsureID(ctx, in.CorrelationID())

		start := time.Now()
		res, out, err := h(ctx, req, in)
		elapsed := time.Since(start)

		status := metrics.StatusOK
		if err != nil || (res != nil && res.IsError) {
			status = metrics.StatusError
		}
		metrics.ObserveToolCall(name, status, elapsed)
		logger.ToolfCtx(ctx, name, "调用完成 status=%s latency=%dms", status, elapsed.Milliseconds())

		return res, out, err
	}
	mcp.AddTool(s, tool, wrapped)
}
