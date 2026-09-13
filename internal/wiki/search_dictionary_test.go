package wiki

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
)

func TestLoadConfigGSEDictionary(t *testing.T) {
	for _, tc := range []struct {
		name, field, want string
		wantErr           bool
	}{
		{"default", "", "zh", false},
		{"full", `,"gse_dictionary":"zh"`, "zh", false},
		{"simplified", `,"gse_dictionary":" ZH_S "`, "zh_s", false},
		{"invalid", `,"gse_dictionary":"zh_t"`, "", true},
		{"path", `,"gse_dictionary":"/tmp/dictionary.txt"`, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "wiki.json")
			raw := fmt.Sprintf(`{"mode":"local","local":{"root":".","tokenizer":"gse"%s}}`, tc.field)
			if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
				t.Fatal(err)
			}
			cfg, err := LoadConfig(path)
			if (err != nil) != tc.wantErr {
				t.Fatalf("LoadConfig error = %v, wantError %v", err, tc.wantErr)
			}
			if err == nil && cfg.Local.GSEDictionary != tc.want {
				t.Fatalf("dictionary = %q, want %q", cfg.Local.GSEDictionary, tc.want)
			}
		})
	}
}

func TestLocalSearcherDictionaryIsolationConcurrent(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "wiki"), 0o700); err != nil {
		t.Fatal(err)
	}
	names := []string{"", "zh_s", "zh", "zh_s", "zh", "zh_s"}
	searchers := make([]*LocalSearcher, len(names))
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i, name := range names {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			s, err := NewLocalSearcher(LocalConfig{Root: root, Tokenizer: SearchTokenizerGSE, GSEDictionary: name})
			if err != nil {
				t.Error(err)
				return
			}
			searchers[i] = s
			for j := 0; j < 20; j++ {
				if got := s.tokenizer.Terms("北京大学"); !reflect.DeepEqual(got, []string{"北京", "大学", "北京大学"}) {
					t.Errorf("dictionary %q terms = %v", name, got)
				}
			}
			doc := localDocument{content: "苹果 苹果 苹果"}
			s.prepareDocument(&doc)
			if got := countSearchTerm(doc.contentTerms, "苹果"); got != 3 {
				t.Errorf("dictionary %q body frequency = %d", name, got)
			}
		}()
	}
	close(start)
	wg.Wait()
	if t.Failed() {
		t.FailNow()
	}
	full := searchers[0].tokenizer.(*gseSearchTokenizer).segmenter.Dictionary()
	small := searchers[1].tokenizer.(*gseSearchTokenizer).segmenter.Dictionary()
	if full == small || small.NumTokens() >= full.NumTokens() {
		t.Fatal("simplified dictionary was not selected independently")
	}
	for i, s := range searchers {
		want := full
		if names[i] == "zh_s" {
			want = small
		}
		if s.tokenizer.(*gseSearchTokenizer).segmenter.Dictionary() != want {
			t.Fatalf("dictionary %q is not shared with matching instances", names[i])
		}
	}
}

func TestTenantGSEDictionaryDoesNotInherit(t *testing.T) {
	root := t.TempDir()
	cfg := LocalConfig{Root: root, GSEDictionary: "zh_s", Users: map[string]LocalConfig{
		"default": {Root: root},
		"simple":  {Root: root, GSEDictionary: " ZH_S "},
	}}
	if err := normalizeLocalConfig(&cfg, "."); err != nil {
		t.Fatal(err)
	}
	if cfg.Users["default"].GSEDictionary != "zh" || cfg.Users["simple"].GSEDictionary != "zh_s" {
		t.Fatal("tenant dictionary default or normalization changed")
	}
	cfg.Users["invalid"] = LocalConfig{Root: root, GSEDictionary: "invalid"}
	if err := normalizeLocalConfig(&cfg, "."); err == nil {
		t.Fatal("invalid tenant dictionary accepted")
	}
}
