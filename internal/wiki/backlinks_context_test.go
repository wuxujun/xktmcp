package wiki

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/wuxujun/xktmcp/internal/model"
)

func TestBuildBacklinksPreservesFirstContextAndAmbiguousTitles(t *testing.T) {
	docs := []localDocument{
		{path: "/wiki/one.md", result: model.WikiSearchResult{PageID: "one", Title: "Shared"}},
		{path: "/wiki/two.md", result: model.WikiSearchResult{PageID: "two", Title: "Shared"}},
		{path: "/wiki/source.md", result: model.WikiSearchResult{PageID: "source", Title: "Source"}, content: "[external](https://example.com/one.md)\nfirst [[Shared]] and [one](one.md#section)\nlater [[one]]\n[[source]]"},
	}
	got, err := buildBacklinks(context.Background(), docs)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]model.WikiBacklink{
		"one": {{SourcePageID: "source", SourceTitle: "Source", Context: "first [[Shared]] and [one](one.md#section)"}},
		"two": {{SourcePageID: "source", SourceTitle: "Source", Context: "first [[Shared]] and [one](one.md#section)"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("backlinks=%v, want %v", got, want)
	}
}

func TestBuildBacklinksHonorsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := buildBacklinks(ctx, []localDocument{{content: "[[target]]"}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v", err)
	}
}
