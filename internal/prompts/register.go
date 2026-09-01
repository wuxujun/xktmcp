package prompts

import (
	"context"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// RegisterAll 注册可由 MCP 客户端通过 prompts/list 发现、通过 prompts/get
// 展开的业务提示模板。Prompt 只描述工作流，实际数据仍由对应 Tool 获取。
func RegisterAll(server *mcp.Server, enabledTools ...map[string]bool) {
	var enabled map[string]bool
	if len(enabledTools) > 0 {
		enabled = enabledTools[0]
	}
	if toolEnabled(enabled, "student_search") {
		server.AddPrompt(&mcp.Prompt{
			Name:        "student_inquiry",
			Title:       "学员信息查询",
			Description: "查询学员档案、订单或考试信息，并处理同名学员歧义",
			Arguments: []*mcp.PromptArgument{
				{Name: "student", Description: "学员姓名、手机号或其他检索线索", Required: true},
				{Name: "question", Description: "希望了解的问题，例如档案、订单或考试成绩", Required: true},
			},
		}, studentInquiry)
	}

	if toolEnabled(enabled, "wiki_search") {
		server.AddPrompt(&mcp.Prompt{
			Name:        "wiki_research",
			Title:       "Wiki 知识检索",
			Description: "检索 Wiki，读取相关词条并基于知识库回答问题",
			Arguments: []*mcp.PromptArgument{
				{Name: "query", Description: "需要检索和回答的问题", Required: true},
				{Name: "category", Description: "可选的 Wiki 分类", Required: false},
			},
		}, wikiResearch)
	}

	if toolEnabled(enabled, "staff_search") {
		server.AddPrompt(&mcp.Prompt{
			Name:        "organization_inquiry",
			Title:       "员工与组织信息查询",
			Description: "查询员工、教师、校区、院系、课程及其关系",
			Arguments: []*mcp.PromptArgument{
				{Name: "query", Description: "需要查询的员工或组织问题", Required: true},
			},
		}, organizationInquiry)
	}
}

func toolEnabled(enabled map[string]bool, name string) bool {
	return enabled == nil || enabled[name]
}

func studentInquiry(_ context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
	student := req.Params.Arguments["student"]
	question := req.Params.Arguments["question"]
	return userPrompt("学员信息查询工作流", fmt.Sprintf(
		"请回答关于学员 %q 的问题：%s。先调用 student_search 定位学员；如有同名或多条匹配，先请用户确认。获得准确 ID 后，再按问题调用 student_get、student_order 或 student_exam。只依据工具返回结果回答，未查到时明确说明。",
		student, question,
	)), nil
}

func wikiResearch(_ context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
	query := req.Params.Arguments["query"]
	category := strings.TrimSpace(req.Params.Arguments["category"])
	categoryInstruction := ""
	if category != "" {
		categoryInstruction = fmt.Sprintf("检索时限定分类 %q。", category)
	}
	return userPrompt("Wiki 知识检索工作流", fmt.Sprintf(
		"请研究并回答：%s。%s先调用 wiki_search 查找相关词条，再对最相关结果调用 wiki_get_page 获取正文；需要理解知识结构或引用关系时，可调用 wiki_list_tree 或 wiki_get_backlinks。只依据 Wiki 返回内容作答，并说明信息不足之处。",
		query, categoryInstruction,
	)), nil
}

func organizationInquiry(_ context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
	query := req.Params.Arguments["query"]
	return userPrompt("员工与组织信息查询工作流", fmt.Sprintf(
		"请回答员工或组织相关问题：%s。调用 staff_search 获取员工、教师、校区、学院、部门、课程或专业信息；只依据工具返回的 context 和 sources 回答。遇到重名、歧义或信息不足时先澄清，不要猜测。",
		query,
	)), nil
}

func userPrompt(description, text string) *mcp.GetPromptResult {
	return &mcp.GetPromptResult{
		Description: description,
		Messages: []*mcp.PromptMessage{
			{Role: mcp.Role("user"), Content: &mcp.TextContent{Text: text}},
		},
	}
}
