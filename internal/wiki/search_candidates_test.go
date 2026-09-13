package wiki

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestGSECandidatesPreserveTitleSubstringMatches(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "wiki"), 0o700); err != nil {
		t.Fatal(err)
	}
	for name, title := range map[string]string{"a.md": "go", "b.md": "golang"} {
		if err := os.WriteFile(filepath.Join(root, "wiki", name), []byte("---\ntitle: "+title+"\nsummary: reference\n---\nreference"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	s, err := NewLocalSearcher(LocalConfig{Root: root, Tokenizer: SearchTokenizerGSE})
	if err != nil {
		t.Fatal(err)
	}
	indexed, err := s.SearchWiki(context.Background(), "", "go", "", 5)
	if err != nil {
		t.Fatal(err)
	}
	s.termIndex = nil // Same scoring and data, without candidate pruning.
	full, err := s.SearchWiki(context.Background(), "", "go", "", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(full) != 2 {
		t.Fatalf("full scan returned %v, want both titles", full)
	}
	if !reflect.DeepEqual(indexed, full) {
		t.Fatalf("indexed = %v; full scan = %v", indexed, full)
	}
}
