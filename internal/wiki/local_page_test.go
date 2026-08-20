package wiki

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestLocalSearcherGetPageByIDAndTitle(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "wiki", "concepts")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	article := "---\npage_id: concept/pbl\ntitle: PBL 概览\ncategory: concepts\nsummary: 项目式学习\nversion: 3\nauthor: editor\ncreated: 2026-08-01\nupdated: 2026-08-19T12:00:00Z\n---\n\n# PBL 概览\n\n完整正文。"
	if err := os.WriteFile(filepath.Join(dir, "pbl.md"), []byte(article), 0o600); err != nil {
		t.Fatal(err)
	}
	searcher, err := NewLocalSearcher(LocalConfig{Root: root, ContentDirs: []string{"wiki"}})
	if err != nil {
		t.Fatal(err)
	}

	page, err := searcher.GetPage(context.Background(), "", "concept/pbl", "")
	if err != nil {
		t.Fatal(err)
	}
	if page.Title != "PBL 概览" || page.Version != 3 || page.Author != "editor" {
		t.Fatalf("unexpected page: %+v", page)
	}
	if page.Content != "# PBL 概览\n\n完整正文。" || page.CreatedAt.IsZero() || page.UpdatedAt.IsZero() {
		t.Fatalf("unexpected page content or timestamps: %+v", page)
	}
	byTitle, err := searcher.GetPage(context.Background(), "", "", "pbl 概览")
	if err != nil || byTitle.PageID != "concept/pbl" {
		t.Fatalf("GetPage by title = %+v, err=%v", byTitle, err)
	}
}
