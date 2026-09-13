package wiki

import (
	"context"
	"os"
	"runtime"
	"sort"
	"testing"
	"time"
)

// Opt-in, read-only measurement of a local corpus. Never logs document content,
// titles or result IDs. Heap deltas are process-level estimates, not RSS.
func TestLocalWikiCorpusProfile(t *testing.T) {
	path := os.Getenv("WIKI_PROFILE_CONFIG")
	if path == "" {
		t.Skip("set WIKI_PROFILE_CONFIG to explicitly enable local corpus profiling")
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Mode != ModeLocal {
		t.Fatal("profiling requires local wiki mode")
	}
	localCfg := cfg.Local
	profileUser := os.Getenv("WIKI_PROFILE_USER")
	if profileUser == "" && len(localCfg.Users) > 0 {
		if _, ok := localCfg.Users["0002"]; ok {
			profileUser = "0002"
		}
	}
	if profileUser != "" {
		if userCfg, ok := localCfg.Users[profileUser]; ok {
			localCfg = userCfg
			t.Logf("profiling configured user %q (tokenizer=%s, dict=%s, root=%s)",
				profileUser, localCfg.Tokenizer, localCfg.GSEDictionary, localCfg.Root)
		}
	}

	heap := func() int64 {
		runtime.GC()
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		return int64(m.HeapAlloc)
	}
	before := heap()
	started := time.Now()
	tokenizer, err := newSearchTokenizerWithDictionary(localCfg.Tokenizer, localCfg.GSEDictionary)
	if err != nil {
		t.Fatal(err)
	}
	dictionaryTime := time.Since(started)
	afterDictionary := heap()
	// Isolate snapshot search from scheduled refresh during measurement.
	localCfg.RefreshIntervalSeconds = 3600
	started = time.Now()
	s, err := NewLocalSearcher(localCfg)
	if err != nil {
		t.Fatal(err)
	}
	indexTime := time.Since(started)
	afterIndex := heap()
	if len(s.documents) == 0 {
		t.Fatal("no indexed documents")
	}

	backlinksStarted := time.Now()
	_, _ = buildBacklinks(context.Background(), s.documents)
	backlinksTime := time.Since(backlinksStarted)

	termIndexStarted := time.Now()
	_ = buildTermIndex(s.documents)
	termIndexTime := time.Since(termIndexStarted)

	var bodyBytes, termSlots int
	for _, doc := range s.documents {
		bodyBytes += len(doc.content)
		termSlots += len(doc.contentTerms)
	}
	t.Logf("documents=%d body_bytes=%d body_term_slots=%d dictionary_init=%s index_build=%s backlinks_stage=%s term_index_stage=%s dictionary_heap_delta_bytes=%d snapshot_heap_delta_bytes=%d",
		len(s.documents), bodyBytes, termSlots, dictionaryTime, indexTime, backlinksTime, termIndexTime, afterDictionary-before, afterIndex-afterDictionary)
	queries := []string{
		s.documents[0].result.Title,
		s.documents[len(s.documents)/2].result.Title,
		s.documents[len(s.documents)-1].result.Title,
		"zzxxyywwqq",
	}
	for i, query := range queries {
		if query == "" {
			t.Fatalf("sample %d has an empty query", i)
		}
		// Warm query execution before collecting wall-clock samples.
		if _, err := s.SearchWiki(context.Background(), "", query, "", 5); err != nil {
			t.Fatal(err)
		}
		samples := make([]time.Duration, 100)
		resultCount := 0
		for j := range samples {
			started := time.Now()
			results, err := s.SearchWiki(context.Background(), "", query, "", 5)
			samples[j] = time.Since(started)
			if err != nil {
				t.Fatal(err)
			}
			resultCount = len(results)
		}
		if (i < 3 && resultCount == 0) || (i == 3 && resultCount != 0) {
			t.Fatalf("sample %d has unexpected result count %d", i, resultCount)
		}
		sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
		t.Logf("sample=%d results=%d samples=100 p50=%s p95=%s", i, resultCount, samples[49], samples[94])
	}
	runtime.KeepAlive(tokenizer)
	runtime.KeepAlive(s)
}

// TestLocalWikiDictionaryComparison compares search recall and ranking between
// GSE 'zh' and 'zh_s' dictionaries on real corpus business queries.
func TestLocalWikiDictionaryComparison(t *testing.T) {
	path := os.Getenv("WIKI_PROFILE_CONFIG")
	if path == "" {
		t.Skip("set WIKI_PROFILE_CONFIG to explicitly enable local corpus profiling")
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Mode != ModeLocal {
		t.Fatal("profiling requires local wiki mode")
	}
	localCfg := cfg.Local
	profileUser := os.Getenv("WIKI_PROFILE_USER")
	if profileUser == "" && len(localCfg.Users) > 0 {
		if _, ok := localCfg.Users["0002"]; ok {
			profileUser = "0002"
		}
	}
	if profileUser != "" {
		if userCfg, ok := localCfg.Users[profileUser]; ok {
			localCfg = userCfg
		}
	}
	localCfg.RefreshIntervalSeconds = 3600

	// 1. Build searcher with ZH (both simplified & traditional)
	cfgZH := localCfg
	cfgZH.Tokenizer = SearchTokenizerGSE
	cfgZH.GSEDictionary = GSEDictionaryZH
	searcherZH, err := NewLocalSearcher(cfgZH)
	if err != nil {
		t.Fatalf("build ZH searcher: %v", err)
	}

	// 2. Build searcher with ZHS (simplified only)
	cfgZHS := localCfg
	cfgZHS.Tokenizer = SearchTokenizerGSE
	cfgZHS.GSEDictionary = GSEDictionaryZHS
	searcherZHS, err := NewLocalSearcher(cfgZHS)
	if err != nil {
		t.Fatalf("build ZHS searcher: %v", err)
	}

	ctx := context.Background()

	// 3. Define categorized business queries
	queryCategories := map[string][]string{
		"繁体中文": {
			"課程", "項目制", "寫作", "競賽", "資料",
			"商業競賽", "學術顧問", "科學無國界", "旅行指南",
		},
		"专有名词与赛事": {
			"PBL", "约翰洛克", "Thinktown", "FBLA", "NEC",
			"沃顿商赛", "学术顾问", "科学无国界",
		},
		"英文与数字混合": {
			"1920年代", "8–10年级", "800–1000字", "AP课程", "PBL写作",
		},
		"简体通用业务短语": {
			"参赛资格", "评审标准", "辅导方案", "历史旅行指南", "学习产品体系",
		},
	}

	type comparisonStat struct {
		total        int
		top1Match    int
		top5Match    int
		zhTotalHits  int
		zhsTotalHits int
	}

	overallStat := comparisonStat{}
	for category, queries := range queryCategories {
		catStat := comparisonStat{}
		t.Logf("=== 类别: %s (%d 个查询) ===", category, len(queries))
		for _, q := range queries {
			resZH, errZH := searcherZH.SearchWiki(ctx, "", q, "", 5)
			if errZH != nil {
				t.Fatalf("ZH query %q failed: %v", q, errZH)
			}
			resZHS, errZHS := searcherZHS.SearchWiki(ctx, "", q, "", 5)
			if errZHS != nil {
				t.Fatalf("ZHS query %q failed: %v", q, errZHS)
			}

			catStat.total++
			catStat.zhTotalHits += len(resZH)
			catStat.zhsTotalHits += len(resZHS)

			top1Same := false
			if len(resZH) == 0 && len(resZHS) == 0 {
				top1Same = true
			} else if len(resZH) > 0 && len(resZHS) > 0 && resZH[0].PageID == resZHS[0].PageID {
				top1Same = true
			}
			if top1Same {
				catStat.top1Match++
			}

			top5Same := len(resZH) == len(resZHS)
			if top5Same {
				for i := range resZH {
					if resZH[i].PageID != resZHS[i].PageID {
						top5Same = false
						break
					}
				}
			}
			if top5Same {
				catStat.top5Match++
			} else {
				t.Logf("[差异] query=%q ZH(hits=%d) vs ZHS(hits=%d)", q, len(resZH), len(resZHS))
				if len(resZH) > 0 {
					t.Logf("  ZH Top-1: %s (score=%.1f)", resZH[0].Title, resZH[0].Score)
				}
				if len(resZHS) > 0 {
					t.Logf("  ZHS Top-1: %s (score=%.1f)", resZHS[0].Title, resZHS[0].Score)
				}
			}
		}
		t.Logf("类别 [%s] 统计: 总数=%d Top1一致率=%.1f%% Top5完全一致率=%.1f%% ZH总召回=%d ZHS总召回=%d",
			category, catStat.total,
			float64(catStat.top1Match)/float64(catStat.total)*100,
			float64(catStat.top5Match)/float64(catStat.total)*100,
			catStat.zhTotalHits, catStat.zhsTotalHits)

		overallStat.total += catStat.total
		overallStat.top1Match += catStat.top1Match
		overallStat.top5Match += catStat.top5Match
		overallStat.zhTotalHits += catStat.zhTotalHits
		overallStat.zhsTotalHits += catStat.zhsTotalHits
	}

	t.Logf("=== 总体对比总结 === 总查询=%d Top1一致率=%.1f%% Top5完全一致率=%.1f%% ZH总召回=%d ZHS总召回=%d",
		overallStat.total,
		float64(overallStat.top1Match)/float64(overallStat.total)*100,
		float64(overallStat.top5Match)/float64(overallStat.total)*100,
		overallStat.zhTotalHits, overallStat.zhsTotalHits)
}
