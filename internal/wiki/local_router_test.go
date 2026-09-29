package wiki

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/wuxujun/xktmcp/internal/trace"
)

func TestLocalRouterIsolatesUsers(t *testing.T) {
	defaultRoot := createRouterWiki(t, "default", "公共手册", "公共内容")
	userARoot := createRouterWiki(t, "user-a", "甲用户手册", "苹果规则")
	userBRoot := createRouterWiki(t, "user-b", "乙用户手册", "香蕉规则")

	router, err := NewLocalRouter(LocalConfig{
		Root: defaultRoot,
		Users: map[string]LocalConfig{
			"user-a": {Root: userARoot},
			"user-b": {Root: userBRoot},
		},
		RequireUserMapping: true,
	})
	if err != nil {
		t.Fatalf("NewLocalRouter returned error: %v", err)
	}

	resultsA, err := router.SearchWiki(context.Background(), "user-a", "苹果", "", 5)
	if err != nil || len(resultsA) != 1 || resultsA[0].Title != "甲用户手册" {
		t.Fatalf("user-a results = %#v, err=%v", resultsA, err)
	}
	resultsB, err := router.SearchWiki(context.Background(), "user-b", "苹果", "", 5)
	if err != nil {
		t.Fatalf("user-b search returned error: %v", err)
	}
	if len(resultsB) != 0 {
		t.Fatalf("user-b saw user-a content: %#v", resultsB)
	}
	for _, userID := range []string{"", "unknown"} {
		if _, err := router.SearchWiki(context.Background(), userID, "公共", "", 5); !errors.Is(err, ErrUserWikiNotConfigured) {
			t.Fatalf("user %q error = %v, want ErrUserWikiNotConfigured", userID, err)
		}
	}
}

func TestLocalRouterFallsBackToDefault(t *testing.T) {
	defaultRoot := createRouterWiki(t, "default", "公共手册", "公共内容")
	router, err := NewLocalRouter(LocalConfig{Root: defaultRoot})
	if err != nil {
		t.Fatalf("NewLocalRouter returned error: %v", err)
	}
	results, err := router.SearchWiki(context.Background(), "unknown", "公共", "", 5)
	if err != nil || len(results) != 1 {
		t.Fatalf("default results = %#v, err=%v", results, err)
	}
}

func TestLocalRouterIsolatesResources(t *testing.T) {
	defaultRoot := createRouterWikiWithPageID(t, "default", "公共手册", "公共内容 130****0000")
	userARoot := createRouterWikiWithPageID(t, "user-a", "甲手册", "甲内容 138****5678")
	userBRoot := createRouterWikiWithPageID(t, "user-b", "乙手册", "乙内容 139****5678")
	router, err := NewLocalRouter(LocalConfig{
		Root: defaultRoot,
		Users: map[string]LocalConfig{
			"user-a": {Root: userARoot},
			"user-b": {Root: userBRoot},
		},
		RequireUserMapping: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	catalogA, err := router.ListResources(context.Background(), "user-a", 10)
	if err != nil || len(catalogA.Items) != 1 || catalogA.Items[0].Name != "甲手册" {
		t.Fatalf("catalog=%+v err=%v", catalogA, err)
	}
	uri := catalogA.Items[0].URI
	pageA, err := router.ReadPageResource(context.Background(), "user-a", uri)
	if err != nil || pageA != "甲内容 138****5678" {
		t.Fatalf("pageA=%q err=%v", pageA, err)
	}
	pageB, err := router.ReadPageResource(context.Background(), "user-b", uri)
	if err != nil || pageB != "乙内容 139****5678" {
		t.Fatalf("pageB=%q err=%v", pageB, err)
	}
	for _, userID := range []string{"", "unknown"} {
		if _, err := router.ListResources(context.Background(), userID, 10); !errors.Is(err, ErrUserWikiNotConfigured) {
			t.Fatalf("user %q error = %v, want ErrUserWikiNotConfigured", userID, err)
		}
	}
}

func TestLocalRouterResourcesFallBackToDefault(t *testing.T) {
	defaultRoot := createRouterWikiWithPageID(t, "default", "公共手册", "公共内容 130****0000")
	router, err := NewLocalRouter(LocalConfig{Root: defaultRoot, RequireUserMapping: false})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := router.ListResources(context.Background(), "unknown", 10)
	if err != nil || len(catalog.Items) != 1 || catalog.Items[0].Name != "公共手册" {
		t.Fatalf("catalog=%+v err=%v", catalog, err)
	}
	page, err := router.ReadPageResource(context.Background(), "unknown", catalog.Items[0].URI)
	if err != nil || page != "公共内容 130****0000" {
		t.Fatalf("page=%q err=%v", page, err)
	}
}

func TestLocalRouterIsolatesUserBacklinks(t *testing.T) {
	userARoot := createBacklinkRouterWiki(t, "user-a", "甲来源", "甲目标", "甲内容")
	userBRoot := createBacklinkRouterWiki(t, "user-b", "乙来源", "乙目标", "乙内容")

	router, err := NewLocalRouter(LocalConfig{
		Root: userARoot,
		Users: map[string]LocalConfig{
			"user-a": {Root: userARoot},
			"user-b": {Root: userBRoot},
		},
		RequireUserMapping: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	linksA, err := router.GetBacklinks(context.Background(), "user-a", "target")
	if err != nil {
		t.Fatal(err)
	}
	if len(linksA) != 1 || linksA[0].SourceTitle != "甲来源" {
		t.Fatalf("user-a backlinks = %+v", linksA)
	}
	linksB, err := router.GetBacklinks(context.Background(), "user-b", "target")
	if err != nil {
		t.Fatal(err)
	}
	if len(linksB) != 1 || linksB[0].SourceTitle != "乙来源" {
		t.Fatalf("user-b backlinks = %+v", linksB)
	}
}

func createBacklinkRouterWiki(t *testing.T, name, sourceTitle, targetTitle, contextLine string) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), name)
	dir := filepath.Join(root, "wiki")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(fileName, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, fileName), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("target.md", "---\npage_id: target\ntitle: "+targetTitle+"\n---\nTarget\n")
	write("source.md", "---\npage_id: source\ntitle: "+sourceTitle+"\n---\n"+contextLine+" [target](target.md).\n")
	return root
}

func createRouterWiki(t *testing.T, name, title, content string) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(filepath.Join(root, "wiki"), 0o755); err != nil {
		t.Fatal(err)
	}
	article := "---\ntitle: " + title + "\n---\n\n" + content + "\n"
	if err := os.WriteFile(filepath.Join(root, "wiki", "article.md"), []byte(article), 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

func createRouterWikiWithPageID(t *testing.T, name, title, content string) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(filepath.Join(root, "wiki"), 0o755); err != nil {
		t.Fatal(err)
	}
	article := "---\npage_id: shared-page\ntitle: " + title + "\n---\n\n" + content + "\n"
	if err := os.WriteFile(filepath.Join(root, "wiki", "article.md"), []byte(article), 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestLocalRouterEnforcesTrustedPrincipalForNetworkRequests(t *testing.T) {
	defaultRoot := createRouterWiki(t, "default", "公共手册", "公共内容")
	userARoot := createRouterWiki(t, "user-a", "甲用户手册", "苹果规则")
	userBRoot := createRouterWiki(t, "user-b", "乙用户手册", "香蕉规则")

	// 1. 严格模式：RequireUserMapping = true
	routerStrict, err := NewLocalRouter(LocalConfig{
		Root: defaultRoot,
		Users: map[string]LocalConfig{
			"user-a": {Root: userARoot},
			"user-b": {Root: userBRoot},
		},
		RequireUserMapping: true,
	})
	if err != nil {
		t.Fatalf("NewLocalRouter returned error: %v", err)
	}

	netCtx := trace.WithRequestOrigin(context.Background(), trace.OriginNetwork)

	// 场景 A: 外部网络请求没有可信认证主体 (principal == "")，试图传入 user-a
	// 应直接拒绝，返回 ErrUserWikiNotConfigured，防止 IDOR 越权
	if _, err := routerStrict.SearchWiki(netCtx, "user-a", "苹果", "", 5); !errors.Is(err, ErrUserWikiNotConfigured) {
		t.Fatalf("网络请求缺少可信主体应被拒绝, 实际 err=%v", err)
	}

	// 场景 B: 外部网络请求持有合法可信主体 user-a，访问 user-a
	ctxUserA := trace.WithAuthenticatedUserID(netCtx, "user-a")
	resA, err := routerStrict.SearchWiki(ctxUserA, "user-a", "苹果", "", 5)
	if err != nil || len(resA) != 1 || resA[0].Title != "甲用户手册" {
		t.Fatalf("持有可信主体 user-a 应成功访问, 实际 res=%+v, err=%v", resA, err)
	}
	// userID 留空时应自动采用可信主体
	resAAuto, err := routerStrict.SearchWiki(ctxUserA, "", "苹果", "", 5)
	if err != nil || len(resAAuto) != 1 || resAAuto[0].Title != "甲用户手册" {
		t.Fatalf("持有可信主体且 userID 为空时应自动采用, 实际 res=%+v, err=%v", resAAuto, err)
	}

	// 场景 C: 外部网络请求持有可信主体 user-a，试图指定访问 user-b
	if _, err := routerStrict.SearchWiki(ctxUserA, "user-b", "香蕉", "", 5); !errors.Is(err, ErrUserWikiNotConfigured) {
		t.Fatalf("可信主体与请求用户冲突应被拒绝, 实际 err=%v", err)
	}

	// 2. 非严格模式：RequireUserMapping = false
	routerNonStrict, err := NewLocalRouter(LocalConfig{
		Root: defaultRoot,
		Users: map[string]LocalConfig{
			"user-a": {Root: userARoot},
			"user-b": {Root: userBRoot},
		},
		RequireUserMapping: false,
	})
	if err != nil {
		t.Fatalf("NewLocalRouter returned error: %v", err)
	}

	// 场景 D: 外部网络请求没有可信主体，指定 user-a，应回退至公共默认 Wiki，绝不泄露 user-a 的私有内容
	resFallback, err := routerNonStrict.SearchWiki(netCtx, "user-a", "公共", "", 5)
	if err != nil || len(resFallback) != 1 || resFallback[0].Title != "公共手册" {
		t.Fatalf("未认证网络请求在非严格模式下应回退到公共 Wiki, 实际 res=%+v, err=%v", resFallback, err)
	}
	// 验证它绝对查不到 user-a 的私有内容
	resPrivateLeak, err := routerNonStrict.SearchWiki(netCtx, "user-a", "苹果", "", 5)
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if len(resPrivateLeak) != 0 {
		t.Fatalf("未认证网络请求泄露了私有内容: %+v", resPrivateLeak)
	}
}
