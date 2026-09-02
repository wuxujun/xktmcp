package wiki

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPageResourceURIRoundTrip(t *testing.T) {
	const pageID = "wiki/topics/student-guide"
	const wantURI = "wiki://page/d2lraS90b3BpY3Mvc3R1ZGVudC1ndWlkZQ"
	uri, err := PageResourceURI(pageID)
	if err != nil || uri != wantURI {
		t.Fatalf("uri=%q err=%v", uri, err)
	}
	got, err := ParsePageResourceURI(uri)
	if err != nil || got != pageID {
		t.Fatalf("pageID=%q err=%v", got, err)
	}
}

func TestParsePageResourceURIRejectsNonCanonicalInput(t *testing.T) {
	for _, uri := range []string{"", "wiki://page/", "wiki://raw/file.md", "wiki://page/%%%", "wiki://page/YQ==", "wiki://page/YQ?x=1", "wiki://page/YQ#x", "wiki://page/YQ/extra", "WIKI://page/YQ"} {
		t.Run(uri, func(t *testing.T) {
			if _, err := ParsePageResourceURI(uri); !errors.Is(err, ErrResourceNotFound) {
				t.Fatalf("error=%v, want ErrResourceNotFound", err)
			}
		})
	}
}

func TestLocalSearcherListResourcesSortsRedactsAndTruncates(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "wiki")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeResourcePage(t, dir, "a.md", "---\npage_id: a-page\ntitle: A Page\ncategory: guides\nsummary: A summary\n---\n\nA content.")
	writeResourcePage(t, dir, "z.md", "---\npage_id: z-page\ntitle: Z 13812345678\ncategory: guides\nsummary: 11010519491231002X\n---\n\nZ content.")
	searcher, err := NewLocalSearcher(LocalConfig{Root: root, ContentDirs: []string{"wiki"}})
	if err != nil {
		t.Fatal(err)
	}

	catalog, err := searcher.ListResources(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if catalog.Total != 2 || !catalog.Truncated || len(catalog.Items) != 1 {
		t.Fatalf("catalog=%+v", catalog)
	}
	if catalog.Items[0].Name != "A Page" || catalog.Items[0].URI != "wiki://page/YS1wYWdl" {
		t.Fatalf("item=%+v", catalog.Items[0])
	}

	catalog, err = searcher.ListResources(context.Background(), 2)
	if err != nil {
		t.Fatal(err)
	}
	if catalog.Items[1].Name != "Z 138****5678" {
		t.Fatalf("item=%+v", catalog.Items[1])
	}
	if strings.Contains(catalog.Items[1].Description, "11010519491231002X") {
		t.Fatalf("description=%q contains raw ID card", catalog.Items[1].Description)
	}
}

func TestLocalSearcherReadPageResourceRedactsContent(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "wiki")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeResourcePage(t, dir, "private.md", "---\npage_id: private-page\ntitle: Private Page\n---\n\nCall 13812345678.")
	searcher, err := NewLocalSearcher(LocalConfig{Root: root, ContentDirs: []string{"wiki"}})
	if err != nil {
		t.Fatal(err)
	}

	uri, err := PageResourceURI("private-page")
	if err != nil {
		t.Fatal(err)
	}
	content, err := searcher.ReadPageResource(context.Background(), uri)
	if err != nil || content != "Call 138****5678." {
		t.Fatalf("content=%q err=%v", content, err)
	}

	missingURI, err := PageResourceURI("missing")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := searcher.ReadPageResource(context.Background(), missingURI); !errors.Is(err, ErrResourceNotFound) {
		t.Fatalf("error=%v, want ErrResourceNotFound", err)
	}
}

func TestLocalSearcherListResourcesRejectsDuplicatePageID(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "wiki")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeResourcePage(t, dir, "one.md", "---\npage_id: duplicate\ntitle: First\n---\n")
	writeResourcePage(t, dir, "two.md", "---\npage_id: duplicate\ntitle: Second\n---\n")
	searcher, err := NewLocalSearcher(LocalConfig{Root: root, ContentDirs: []string{"wiki"}})
	if err != nil {
		t.Fatal(err)
	}

	catalog, err := searcher.ListResources(context.Background(), 2)
	if !errors.Is(err, ErrDuplicateResourcePageID) || catalog.Total != 0 {
		t.Fatalf("catalog=%+v err=%v", catalog, err)
	}
}

func writeResourcePage(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
