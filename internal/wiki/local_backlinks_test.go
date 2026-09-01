package wiki

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestLocalSearcherGetBacklinks(t *testing.T) {
	root := t.TempDir()
	concepts := filepath.Join(root, "wiki", "concepts")
	topics := filepath.Join(root, "wiki", "topics")
	if err := os.MkdirAll(concepts, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(topics, 0o755); err != nil {
		t.Fatal(err)
	}
	target := "---\npage_id: concepts/pbl\ntitle: PBL 概览\n---\n\n目标正文"
	markdownSource := "---\ntitle: 课程设计\n---\n\n参见 [PBL](../concepts/pbl.md#实践)。"
	wikiSource := "---\ntitle: 教学方法\n---\n\n关联 [[concepts/pbl|项目式学习]]。"
	for path, content := range map[string]string{
		filepath.Join(concepts, "pbl.md"):  target,
		filepath.Join(topics, "course.md"): markdownSource,
		filepath.Join(topics, "method.md"): wikiSource,
	} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	searcher, err := NewLocalSearcher(LocalConfig{Root: root, ContentDirs: []string{"wiki"}})
	if err != nil {
		t.Fatal(err)
	}

	links, err := searcher.GetBacklinks(context.Background(), "", "concepts/pbl")
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 2 {
		t.Fatalf("links = %+v, want two backlinks", links)
	}
	if links[0].Context == "" || links[1].Context == "" {
		t.Fatalf("backlink context missing: %+v", links)
	}
}

func TestLocalSearcherIndexesBacklinksDuringRefresh(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "wiki")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "target.md"), []byte("---\npage_id: target\ntitle: Target\n---\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "source.md"), []byte("---\npage_id: source\ntitle: Source\n---\nSee [target](target.md).\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	searcher, err := NewLocalSearcher(LocalConfig{Root: root, ContentDirs: []string{"wiki"}})
	if err != nil {
		t.Fatal(err)
	}
	links, ok := searcher.backlinks["target"]
	if !ok || len(links) != 1 || links[0].SourcePageID != "source" {
		t.Fatalf("backlinks index = %+v, want source backlink for target", searcher.backlinks)
	}
}
