package wiki

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
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

func TestPageResourceURIPreservesMaximumBoundary(t *testing.T) {
	const maxEncodedKeyBytes = 2731
	pageID := strings.Repeat("a", maxResourcePageIDBytes)
	uri, err := PageResourceURI(pageID)
	if err != nil {
		t.Fatalf("PageResourceURI(maximum) error=%v", err)
	}
	key := strings.TrimPrefix(uri, pageResourcePrefix)
	if len(key) != maxEncodedKeyBytes {
		t.Fatalf("encoded key length=%d, want %d", len(key), maxEncodedKeyBytes)
	}
	got, err := ParsePageResourceURI(uri)
	if err != nil || got != pageID {
		t.Fatalf("pageID length=%d err=%v, want maximum round trip", len(got), err)
	}

	if _, err := PageResourceURI(strings.Repeat("a", maxResourcePageIDBytes+1)); !errors.Is(err, ErrResourceNotFound) {
		t.Fatalf("one-byte-over error=%v, want ErrResourceNotFound", err)
	}
}

func TestParsePageResourceURIRejectsOversizedKeyBeforeDecode(t *testing.T) {
	const maxEncodedKeyBytes = 2731
	uri := pageResourcePrefix + strings.Repeat("Y", maxEncodedKeyBytes+1)
	if _, err := ParsePageResourceURI(uri); !errors.Is(err, ErrResourceNotFound) {
		t.Fatalf("error=%v, want ErrResourceNotFound", err)
	}

	allocations := testing.AllocsPerRun(10, func() {
		_, _ = ParsePageResourceURI(uri)
	})
	if allocations != 0 {
		t.Fatalf("oversized key caused %.0f allocations; decoder must not run", allocations)
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

func TestLocalSearcherReadPageResourceReturnsCanceledContext(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "wiki")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeResourcePage(t, dir, "page.md", "---\npage_id: page\ntitle: Page\n---\n\nBody.")
	searcher, err := NewLocalSearcher(LocalConfig{Root: root, ContentDirs: []string{"wiki"}})
	if err != nil {
		t.Fatal(err)
	}
	uri, err := PageResourceURI("page")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := searcher.ReadPageResource(ctx, uri); !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v, want context.Canceled", err)
	}
}

func TestLocalSearcherReadPageResourceChecksCancellationAfterIndexedRead(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "wiki")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeResourcePage(t, dir, "page.md", "---\npage_id: page\ntitle: Page\n---\n\nBody.")
	searcher, err := NewLocalSearcher(LocalConfig{Root: root, ContentDirs: []string{"wiki"}})
	if err != nil {
		t.Fatal(err)
	}
	uri, err := PageResourceURI("page")
	if err != nil {
		t.Fatal(err)
	}
	ctx := newCancelOnNthErrContext(2)

	if _, err := searcher.ReadPageResource(ctx, uri); !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v, want context.Canceled", err)
	}
}

func TestLocalSearcherListResourcesReturnsCanceledForEmptyCatalog(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "wiki")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	searcher, err := NewLocalSearcher(LocalConfig{Root: root, ContentDirs: []string{"wiki"}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := searcher.ListResources(ctx, 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v, want context.Canceled", err)
	}
}

func TestLocalSearcherListResourcesChecksCancellationAcrossPhases(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "wiki")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeResourcePage(t, dir, "page.md", "---\npage_id: page\ntitle: Page\n---\n\nBody.")
	searcher, err := NewLocalSearcher(LocalConfig{
		Root: root, ContentDirs: []string{"wiki"}, RefreshIntervalSeconds: 3600,
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, tt := range []struct {
		name     string
		cancelAt int
	}{
		{name: "after refresh", cancelAt: 2},
		{name: "during descriptor build", cancelAt: 5},
		{name: "after sort", cancelAt: 7},
		{name: "during output", cancelAt: 8},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx := newCancelOnNthErrContext(tt.cancelAt)
			if _, err := searcher.ListResources(ctx, 1); !errors.Is(err, context.Canceled) {
				t.Fatalf("error=%v, want context.Canceled", err)
			}
		})
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

type cancelOnNthErrContext struct {
	context.Context
	mu       sync.Mutex
	calls    int
	cancelAt int
	done     chan struct{}
	canceled bool
}

func newCancelOnNthErrContext(cancelAt int) *cancelOnNthErrContext {
	return &cancelOnNthErrContext{
		Context:  context.Background(),
		cancelAt: cancelAt,
		done:     make(chan struct{}),
	}
}

func (c *cancelOnNthErrContext) Done() <-chan struct{} { return c.done }

func (c *cancelOnNthErrContext) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	if !c.canceled && c.calls >= c.cancelAt {
		close(c.done)
		c.canceled = true
	}
	if c.canceled {
		return context.Canceled
	}
	return nil
}
