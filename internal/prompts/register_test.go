package prompts

import (
	"context"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestWikiResearchPrompt(t *testing.T) {
	result, err := wikiResearch(context.Background(), &mcp.GetPromptRequest{
		Params: &mcp.GetPromptParams{
			Name: "wiki_research",
			Arguments: map[string]string{
				"query":    "退费规则是什么",
				"category": "教务",
			},
		},
	})
	if err != nil {
		t.Fatalf("wikiResearch returned error: %v", err)
	}
	if len(result.Messages) != 1 {
		t.Fatalf("messages count = %d, want 1", len(result.Messages))
	}
	content, ok := result.Messages[0].Content.(*mcp.TextContent)
	if !ok {
		t.Fatalf("content type = %T, want *mcp.TextContent", result.Messages[0].Content)
	}
	for _, want := range []string{"退费规则是什么", "教务", "wiki_search", "wiki_get_page"} {
		if !strings.Contains(content.Text, want) {
			t.Errorf("prompt text does not contain %q: %s", want, content.Text)
		}
	}
}

func TestRegisterAllPromptsAreDiscoverable(t *testing.T) {
	ctx := context.Background()
	server := mcp.NewServer(&mcp.Implementation{Name: "test-server", Version: "1.0.0"}, nil)
	RegisterAll(server)

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1.0.0"}, nil)
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("connect server: %v", err)
	}
	defer serverSession.Close()
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("connect client: %v", err)
	}
	defer clientSession.Close()

	var names []string
	for prompt, err := range clientSession.Prompts(ctx, nil) {
		if err != nil {
			t.Fatalf("list prompts: %v", err)
		}
		names = append(names, prompt.Name)
	}
	if got, want := strings.Join(names, ","), "organization_inquiry,student_inquiry,wiki_research"; got != want {
		t.Fatalf("prompt names = %q, want %q", got, want)
	}

	result, err := clientSession.GetPrompt(ctx, &mcp.GetPromptParams{
		Name: "student_inquiry",
		Arguments: map[string]string{
			"student":  "张三",
			"question": "考试成绩",
		},
	})
	if err != nil {
		t.Fatalf("get prompt: %v", err)
	}
	if len(result.Messages) != 1 {
		t.Fatalf("messages count = %d, want 1", len(result.Messages))
	}
}

func TestRegisterAllFiltersPromptsByTools(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "test-server", Version: "1.0.0"}, nil)
	RegisterAll(server, map[string]bool{"wiki_search": true})
	ctx := context.Background()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer serverSession.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1.0.0"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer clientSession.Close()
	var names []string
	for prompt, err := range clientSession.Prompts(ctx, nil) {
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, prompt.Name)
	}
	if strings.Join(names, ",") != "wiki_research" {
		t.Fatalf("prompts=%v, want wiki_research", names)
	}
}
