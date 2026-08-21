package tools

import (
	"encoding/json"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestAllToolOutputSchemasHaveObjectRoot(t *testing.T) {
	all := []*mcp.Tool{
		RagSearchTool(),
		StaffSearchTool(),
		StudentExamTool(),
		StudentGetTool(),
		StudentOrderTool(),
		StudentSearchTool(),
		WikiGetBacklinksTool(),
		WikiGetPageTool(),
		WikiListTreeTool(),
		WikiSearchTool(),
		WikiUpsertPageTool(),
	}

	for _, tool := range all {
		t.Run(tool.Name, func(t *testing.T) {
			encoded, err := json.Marshal(tool.OutputSchema)
			if err != nil {
				t.Fatalf("marshal output schema: %v", err)
			}
			var schema map[string]any
			if err := json.Unmarshal(encoded, &schema); err != nil {
				t.Fatalf("decode output schema: %v", err)
			}
			if got := schema["type"]; got != "object" {
				t.Fatalf("outputSchema.type = %v, want object; schema=%s", got, encoded)
			}
		})
	}
}
