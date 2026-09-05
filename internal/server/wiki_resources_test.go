package server

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/wuxujun/xktmcp/internal/model"
	"github.com/wuxujun/xktmcp/internal/trace"
	wikibackend "github.com/wuxujun/xktmcp/internal/wiki"
)

func TestWikiResourceCatalogUsesTrustedUserAndRedactsPII(t *testing.T) {
	router := newMultiUserWikiResourceRouter(t)
	ctxA := trace.WithAuthenticatedUserID(context.Background(), "user-a")

	result, err := wikiCatalogHandler(router, 10, "")(ctxA, &mcp.ReadResourceRequest{
		Params: &mcp.ReadResourceParams{URI: "wiki://catalog"},
	})
	if err != nil || len(result.Contents) != 1 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	text := result.Contents[0].Text
	if !strings.Contains(text, "甲手册 138****5678") || strings.Contains(text, "乙手册") || strings.Contains(text, "13800125678") {
		t.Fatalf("catalog=%s", text)
	}
}

func TestWikiResourceTreeUsesTrustedUserAndRedactsPII(t *testing.T) {
	router := newMultiUserWikiResourceRouter(t)
	ctxA := trace.WithAuthenticatedUserID(context.Background(), "user-a")

	result, err := wikiTreeHandler(router)(ctxA, &mcp.ReadResourceRequest{
		Params: &mcp.ReadResourceParams{URI: "wiki://tree"},
	})
	if err != nil || len(result.Contents) != 1 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	text := result.Contents[0].Text
	if !strings.Contains(text, "甲手册 138****5678") || strings.Contains(text, "乙手册") || strings.Contains(text, "13800125678") {
		t.Fatalf("tree=%s", text)
	}
}

func TestWikiResourcePageUsesTrustedUserForSharedURI(t *testing.T) {
	router := newMultiUserWikiResourceRouter(t)
	pageURI, err := wikibackend.PageResourceURI("shared-page")
	if err != nil {
		t.Fatal(err)
	}
	pageHandler := wikiPageHandler(router, "")
	ctxA := trace.WithAuthenticatedUserID(context.Background(), "user-a")
	ctxB := trace.WithAuthenticatedUserID(context.Background(), "user-b")

	result, err := pageHandler(ctxA, &mcp.ReadResourceRequest{Params: &mcp.ReadResourceParams{URI: pageURI}})
	if err != nil || len(result.Contents) != 1 || result.Contents[0].Text != "甲内容 138****5678" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	result, err = pageHandler(ctxB, &mcp.ReadResourceRequest{Params: &mcp.ReadResourceParams{URI: pageURI}})
	if err != nil || len(result.Contents) != 1 || result.Contents[0].Text != "乙内容 139****5678" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestWikiResourceMapsInvalidURIAndUnknownUserToNotFound(t *testing.T) {
	router := newMultiUserWikiResourceRouter(t)
	pageHandler := wikiPageHandler(router, "")
	ctxA := trace.WithAuthenticatedUserID(context.Background(), "user-a")

	_, err := pageHandler(ctxA, &mcp.ReadResourceRequest{Params: &mcp.ReadResourceParams{URI: "wiki://page/%%%"}})
	assertWikiResourceNotFound(t, err)

	pageURI, err := wikibackend.PageResourceURI("shared-page")
	if err != nil {
		t.Fatal(err)
	}
	unknownCtx := trace.WithAuthenticatedUserID(context.Background(), "unknown")
	_, err = pageHandler(unknownCtx, &mcp.ReadResourceRequest{Params: &mcp.ReadResourceParams{URI: pageURI}})
	assertWikiResourceNotFound(t, err)
}

func TestWikiResourceRejectsMissingParamsAndMismatchedFixedURI(t *testing.T) {
	router := newMultiUserWikiResourceRouter(t)
	ctxA := trace.WithAuthenticatedUserID(context.Background(), "user-a")
	tests := []struct {
		name    string
		handler mcp.ResourceHandler
		req     *mcp.ReadResourceRequest
	}{
		{name: "catalog missing params", handler: wikiCatalogHandler(router, 10, ""), req: &mcp.ReadResourceRequest{}},
		{name: "catalog mismatched URI", handler: wikiCatalogHandler(router, 10, ""), req: &mcp.ReadResourceRequest{Params: &mcp.ReadResourceParams{URI: "wiki://tree"}}},
		{name: "tree missing params", handler: wikiTreeHandler(router), req: &mcp.ReadResourceRequest{}},
		{name: "tree mismatched URI", handler: wikiTreeHandler(router), req: &mcp.ReadResourceRequest{Params: &mcp.ReadResourceParams{URI: "wiki://catalog"}}},
		{name: "page missing params", handler: wikiPageHandler(router, ""), req: &mcp.ReadResourceRequest{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := tt.handler(ctxA, tt.req)
			assertWikiResourceNotFound(t, err)
		})
	}
}

func TestWikiResourcePropagatesCanceledContext(t *testing.T) {
	router := newMultiUserWikiResourceRouter(t)
	ctx, cancel := context.WithCancel(trace.WithAuthenticatedUserID(context.Background(), "user-a"))
	cancel()

	_, err := wikiTreeHandler(router)(ctx, &mcp.ReadResourceRequest{Params: &mcp.ReadResourceParams{URI: "wiki://tree"}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v, want context.Canceled", err)
	}
}

func TestWikiResourceMasksUnexpectedBackendError(t *testing.T) {
	backend := failingWikiResourceBackend{err: errors.New("read /private/root failed")}
	ctx := trace.WithAuthenticatedUserID(context.Background(), "user-a")
	pageURI, err := wikibackend.PageResourceURI("shared-page")
	if err != nil {
		t.Fatal(err)
	}

	_, err = wikiPageHandler(backend, "")(ctx, &mcp.ReadResourceRequest{Params: &mcp.ReadResourceParams{URI: pageURI}})
	if err == nil || err.Error() != "read wiki resource failed" || strings.Contains(err.Error(), "/private/root") {
		t.Fatalf("error=%v", err)
	}
}

type failingWikiResourceBackend struct {
	err error
}

func (b failingWikiResourceBackend) ListResources(context.Context, string, int) (wikibackend.ResourceCatalog, error) {
	return wikibackend.ResourceCatalog{}, b.err
}

func (b failingWikiResourceBackend) ReadPageResource(context.Context, string, string) (string, error) {
	return "", b.err
}

func (b failingWikiResourceBackend) ListTree(context.Context, string, string, int) ([]model.WikiNode, error) {
	return nil, b.err
}

func newMultiUserWikiResourceRouter(t *testing.T) *wikibackend.LocalRouter {
	t.Helper()
	defaultRoot := writeWikiResourceFixture(t, "default", "公共手册", "公共内容")
	userARoot := writeWikiResourceFixture(t, "user-a", "甲手册 13800125678", "甲内容 13800125678")
	userBRoot := writeWikiResourceFixture(t, "user-b", "乙手册 13900125678", "乙内容 13900125678")
	router, err := wikibackend.NewLocalRouter(wikibackend.LocalConfig{
		Root: defaultRoot,
		Users: map[string]wikibackend.LocalConfig{
			"user-a": {Root: userARoot},
			"user-b": {Root: userBRoot},
		},
		RequireUserMapping: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return router
}

func writeWikiResourceFixture(t *testing.T, name, title, content string) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), name)
	dir := filepath.Join(root, "wiki")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	page := "---\npage_id: shared-page\ntitle: " + title + "\n---\n\n" + content + "\n"
	if err := os.WriteFile(filepath.Join(dir, "article.md"), []byte(page), 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

func assertWikiResourceNotFound(t *testing.T, err error) {
	t.Helper()
	var rpcErr *jsonrpc.Error
	if !errors.As(err, &rpcErr) || rpcErr.Code != mcp.CodeResourceNotFound {
		t.Fatalf("error=%v, want resource not found", err)
	}
}
