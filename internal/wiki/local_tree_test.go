package wiki

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/wuxujun/xktmcp/internal/model"
)

func TestLocalSearcherListTree(t *testing.T) {
	root := t.TempDir()
	concepts := filepath.Join(root, "wiki", "concepts")
	sources := filepath.Join(root, "wiki", "sources")
	if err := os.MkdirAll(concepts, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(sources, 0o755); err != nil {
		t.Fatal(err)
	}
	article := "---\npage_id: pbl-overview\ntitle: PBL 课程概览\ncategory: concepts\n---\n\n正文"
	if err := os.WriteFile(filepath.Join(concepts, "pbl.md"), []byte(article), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(concepts, "_index.md"), []byte("derived"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "wiki", ".hidden"), 0o755); err != nil {
		t.Fatal(err)
	}

	searcher, err := NewLocalSearcher(LocalConfig{
		Root: root, ContentDirs: []string{"wiki"},
		RefreshIntervalSeconds: 30, MaxFileSizeBytes: 2 << 20,
	})
	if err != nil {
		t.Fatal(err)
	}

	nodes, err := searcher.ListTree(context.Background(), "", "", 2)
	if err != nil {
		t.Fatalf("ListTree returned error: %v", err)
	}
	conceptNode := findNode(nodes, "wiki/concepts")
	if conceptNode == nil {
		t.Fatalf("nodes = %+v, want concepts directory", nodes)
	}
	if !conceptNode.HasChildren || len(conceptNode.Children) != 1 {
		t.Fatalf("concepts node = %+v, want one article child", conceptNode)
	}
	if conceptNode.Children[0].ID != "pbl-overview" || conceptNode.Children[0].Title != "PBL 课程概览" {
		t.Fatalf("article node = %+v", conceptNode.Children[0])
	}
	if findNode(nodes, "wiki/.hidden") != nil {
		t.Fatalf("hidden directory was included: %+v", nodes)
	}
}

func TestLocalSearcherListTreeParentID(t *testing.T) {
	root := t.TempDir()
	concepts := filepath.Join(root, "wiki", "concepts")
	if err := os.MkdirAll(concepts, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(concepts, "one.md"), []byte("# One"), 0o600); err != nil {
		t.Fatal(err)
	}
	searcher, err := NewLocalSearcher(LocalConfig{
		Root: root, ContentDirs: []string{"wiki"},
		RefreshIntervalSeconds: 30, MaxFileSizeBytes: 2 << 20,
	})
	if err != nil {
		t.Fatal(err)
	}

	nodes, err := searcher.ListTree(context.Background(), "", "wiki/concepts", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 || nodes[0].Title != "One" {
		t.Fatalf("nodes = %+v, want one article", nodes)
	}
	if _, err := searcher.ListTree(context.Background(), "", "../raw", 1); err == nil {
		t.Fatal("ListTree accepted parent_id escaping local root")
	}
}

func findNode(nodes []model.WikiNode, id string) *model.WikiNode {
	for i := range nodes {
		if nodes[i].ID == id {
			return &nodes[i]
		}
	}
	return nil
}
