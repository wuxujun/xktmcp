package wiki

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
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
	if _, err := router.SearchWiki(context.Background(), "unknown", "公共", "", 5); !errors.Is(err, ErrUserWikiNotConfigured) {
		t.Fatalf("unknown user error = %v, want ErrUserWikiNotConfigured", err)
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
