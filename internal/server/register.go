package server

import (
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/wuxujun/xktmcp/internal/client"
	"github.com/wuxujun/xktmcp/internal/service"
	"github.com/wuxujun/xktmcp/internal/tools"
)

func RegisterAll(s *mcp.Server) error {
	baseCfg, err := client.LoadConfigFromEnv()
	if err != nil {
		return err
	}

	studentAPI := client.NewStudentAPI(baseCfg)
	studentSvc := service.NewStudentService(studentAPI)

	mcp.AddTool(s, tools.StudentSearchTool(), tools.StudentSearchHandler(studentSvc))
	mcp.AddTool(s, tools.StudentOrderTool(), tools.StudentOrderHandler(studentSvc))
	mcp.AddTool(s, tools.StudentExamTool(), tools.StudentExamHandler(studentSvc))
	mcp.AddTool(s, tools.StudentGetTool(), tools.StudentGetHandler(studentSvc))

	//Rag搜索
	ragAPI := client.NewRagAPI(baseCfg)
	ragSvc := service.NewRagService(ragAPI)
	mcp.AddTool(s, tools.RagSearchTool(), tools.RagSearchHandler(ragSvc))

	return nil
}
