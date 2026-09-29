package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/wuxujun/xktmcp/internal/service"
	"github.com/wuxujun/xktmcp/internal/trace"
	"github.com/wuxujun/xktmcp/internal/wiki"
)

func TestWikiHandlersRejectUnboundNetworkCacheAccess(t *testing.T) {
	makeRoot := func(title string) string {
		t.Helper()
		root := t.TempDir()
		if err := os.Mkdir(filepath.Join(root, "wiki"), 0o700); err != nil {
			t.Fatal(err)
		}
		content := "---\npage_id: shared-page\ntitle: " + title + "\n---\n\nsecurityneedle " + title + "\n"
		if err := os.WriteFile(filepath.Join(root, "wiki", "article.md"), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return root
	}
	publicRoot, privateRoot := makeRoot("PublicManual"), makeRoot("PrivateSecret")
	for _, strict := range []bool{true, false} {
		name := "public_fallback"
		if strict {
			name = "strict_mapping"
		}
		t.Run(name, func(t *testing.T) {
			router, err := wiki.NewLocalRouter(wiki.LocalConfig{
				Root: publicRoot, RequireUserMapping: strict,
				Users: map[string]wiki.LocalConfig{"victim": {Root: privateRoot}},
			})
			if err != nil {
				t.Fatal(err)
			}
			svc := service.NewWikiService(nil, router)
			searchArgs := WikiSearchArgs{Query: "securityneedle", TopK: 5}
			searchArgs.UserID = "victim"
			pageArgs := WikiGetPageArgs{PageID: "shared-page"}
			pageArgs.UserID = "victim"
			treeArgs := WikiListTreeArgs{Depth: 3}
			treeArgs.UserID = "victim"
			for _, tc := range []struct {
				name string
				call func(context.Context) (*mcp.CallToolResult, any, error)
			}{
				{"search", func(ctx context.Context) (*mcp.CallToolResult, any, error) {
					return WikiSearchHandler(svc, "")(ctx, nil, searchArgs)
				}},
				{"page", func(ctx context.Context) (*mcp.CallToolResult, any, error) {
					return WikiGetPageHandler(svc)(ctx, nil, pageArgs)
				}},
				{"tree", func(ctx context.Context) (*mcp.CallToolResult, any, error) {
					return WikiListTreeHandler(svc)(ctx, nil, treeArgs)
				}},
			} {
				t.Run(tc.name, func(t *testing.T) {
					oldCache := wikiCache
					wikiCache = NewMemoryCacheWithOptions(16, 0)
					t.Cleanup(func() { wikiCache.Stop(); wikiCache = oldCache })
					network := trace.WithRequestOrigin(context.Background(), trace.OriginNetwork)
					victim := trace.WithAuthenticatedUserID(network, "victim")
					assertUnbound := func() {
						t.Helper()
						res, data, err := tc.call(network)
						if err != nil || res == nil {
							t.Fatalf("unbound request: result=%v err=%v", res, err)
						}
						if res.IsError != strict {
							t.Fatalf("unbound IsError=%v, want %v", res.IsError, strict)
						}
						encoded, err := json.Marshal(data)
						if err != nil {
							t.Fatal(err)
						}
						if strings.Contains(string(encoded), "PrivateSecret") {
							t.Fatalf("private data leaked: %s", encoded)
						}
						if !strict && !strings.Contains(string(encoded), "PublicManual") {
							t.Fatalf("public fallback missing: %s", encoded)
						}
					}
					// 公共回退不能污染随后合法用户的私有缓存。
					assertUnbound()
					warm, data, err := tc.call(victim)
					if err != nil || warm == nil || warm.IsError {
						t.Fatalf("private request: result=%v err=%v", warm, err)
					}
					encoded, err := json.Marshal(data)
					if err != nil || !strings.Contains(string(encoded), "PrivateSecret") {
						t.Fatalf("private result missing or poisoned: data=%s err=%v", encoded, err)
					}
					// 已预热的私有缓存不能绕过网络请求的主体校验。
					assertUnbound()
					cached, _, err := tc.call(victim)
					if err != nil || cached != warm {
						t.Fatalf("trusted request did not reuse private cache: err=%v", err)
					}
					stdio, _, err := tc.call(context.Background())
					if err != nil || stdio != warm {
						t.Fatalf("stdio cache behavior changed: err=%v", err)
					}
				})
			}
		})
	}
}
