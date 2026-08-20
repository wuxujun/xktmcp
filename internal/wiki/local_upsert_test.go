package wiki

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLocalSearcherUpsertLifecycle(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "wiki"), 0o755); err != nil {
		t.Fatal(err)
	}
	searcher, err := NewLocalSearcher(LocalConfig{Root: root, ContentDirs: []string{"wiki"}})
	if err != nil {
		t.Fatal(err)
	}

	created, err := searcher.UpsertPage(context.Background(), "author-1", "Local Page", "# Local Page\n\ninitial body", "", "", "create")
	if err != nil {
		t.Fatal(err)
	}
	if created.Status != "created" || created.Version != 1 || created.PageID != "wiki/topics/local-page" {
		t.Fatalf("created = %+v", created)
	}
	path := filepath.Join(root, "wiki", "topics", "local-page.md")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	raw = []byte(strings.Replace(string(raw), "title: \"Local Page\"", "title: \"Local Page\"\ncustom_field: keep-me", 1))
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	updated, err := searcher.UpsertPage(context.Background(), "author-2", "Local Page", "replacement body", "guides", "new summary", "update")
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != "updated" || updated.Version != 2 {
		t.Fatalf("updated = %+v", updated)
	}
	appended, err := searcher.UpsertPage(context.Background(), "author-2", "Local Page", "appended body", "", "", "append")
	if err != nil {
		t.Fatal(err)
	}
	if appended.Status != "appended" || appended.Version != 3 {
		t.Fatalf("appended = %+v", appended)
	}
	page, err := searcher.GetPage(context.Background(), "", created.PageID, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(page.Content, "replacement body\n\nappended body") {
		t.Fatalf("page content = %q", page.Content)
	}
	finalRaw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(finalRaw), "custom_field: keep-me") {
		t.Fatalf("unknown frontmatter was not preserved:\n%s", finalRaw)
	}
	logData, err := os.ReadFile(filepath.Join(root, "log.md"))
	if err != nil {
		t.Fatal(err)
	}
	if count := strings.Count(string(logData), "] upsert |"); count != 3 {
		t.Fatalf("activity log entries = %d, want 3:\n%s", count, logData)
	}
	results, err := searcher.SearchWiki(context.Background(), "", "appended body", "", 5)
	if err != nil || len(results) != 1 {
		t.Fatalf("immediate refreshed search results = %+v, err=%v", results, err)
	}
}

func TestLocalSearcherUpsertRejectsPageOutsideWriteDir(t *testing.T) {
	root := t.TempDir()
	concepts := filepath.Join(root, "wiki", "concepts")
	if err := os.MkdirAll(concepts, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(concepts, "protected.md"), []byte("# Protected\n\nbody"), 0o600); err != nil {
		t.Fatal(err)
	}
	searcher, err := NewLocalSearcher(LocalConfig{Root: root, ContentDirs: []string{"wiki"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := searcher.UpsertPage(context.Background(), "", "Protected", "changed", "", "", "update"); err == nil {
		t.Fatal("UpsertPage updated a page outside write_dir")
	}
	if _, err := searcher.UpsertPage(context.Background(), "", "New", "body", "", "", "replace"); err == nil {
		t.Fatal("UpsertPage accepted an invalid mode")
	}
}

func TestLocalSearcherUpsertRejectsSymlinkedWriteDir(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "wiki")); err != nil {
		t.Fatal(err)
	}
	searcher, err := NewLocalSearcher(LocalConfig{Root: root, ContentDirs: []string{"wiki"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := searcher.UpsertPage(context.Background(), "", "Escaped", "body", "", "", "create"); err == nil {
		t.Fatal("UpsertPage accepted a write_dir symlink escaping local.root")
	}
	if _, err := os.Stat(filepath.Join(outside, "topics")); !os.IsNotExist(err) {
		t.Fatalf("UpsertPage created content outside local.root: %v", err)
	}
}
