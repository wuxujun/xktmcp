package server

import (
	"fmt"
	"os"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/wuxujun/xktmcp/internal/service"
	"github.com/wuxujun/xktmcp/internal/tools"
)

func registerFileSearch(s *mcp.Server, enabledTools map[string]bool) error {
	if enabledTools != nil && !fileToolsEnabled(enabledTools) {
		return nil
	}
	root := strings.TrimSpace(os.Getenv("FILE_SEARCH_ROOT"))
	if root == "" {
		if enabledTools != nil {
			return fmt.Errorf("file_search requires FILE_SEARCH_ROOT")
		}
		return nil
	}
	options := service.FileSearchOptions{}
	var err error
	if toolEnabled(enabledTools, "file_search") {
		options, err = fileSearchOptionsFromEnv()
		if err != nil {
			return err
		}
	}
	svc, err := service.NewFileServiceWithOptions(root, options)
	if err != nil {
		return fmt.Errorf("invalid FILE_SEARCH_ROOT: %w", err)
	}
	if toolEnabled(enabledTools, "file_search") {
		addTool(s, tools.FileSearchTool(), tools.FileSearchHandler(svc))
	}
	if toolEnabled(enabledTools, "file_get_info") {
		addTool(s, tools.FileInfoTool(), tools.FileInfoHandler(svc))
	}
	if toolEnabled(enabledTools, "file_read_preview") {
		addTool(s, tools.FilePreviewTool(), tools.FilePreviewHandler(svc))
	}
	return nil
}

func fileSearchOptionsFromEnv() (service.FileSearchOptions, error) {
	tokenizer := strings.ToLower(strings.TrimSpace(os.Getenv("FILE_SEARCH_TOKENIZER")))
	if tokenizer == "" {
		tokenizer = service.FileSearchTokenizerBuiltin
	}
	if tokenizer != service.FileSearchTokenizerBuiltin && tokenizer != service.FileSearchTokenizerGSE {
		return service.FileSearchOptions{}, fmt.Errorf("FILE_SEARCH_TOKENIZER %q is unsupported (want %q or %q)", tokenizer, service.FileSearchTokenizerBuiltin, service.FileSearchTokenizerGSE)
	}
	dictionary := strings.ToLower(strings.TrimSpace(os.Getenv("FILE_SEARCH_GSE_DICTIONARY")))
	if dictionary == "" {
		dictionary = service.FileSearchGSEDictionaryZH
	}
	if tokenizer == service.FileSearchTokenizerGSE && dictionary != service.FileSearchGSEDictionaryZH && dictionary != service.FileSearchGSEDictionaryZHS {
		return service.FileSearchOptions{}, fmt.Errorf("FILE_SEARCH_GSE_DICTIONARY %q is unsupported (want %q or %q)", dictionary, service.FileSearchGSEDictionaryZH, service.FileSearchGSEDictionaryZHS)
	}
	return service.FileSearchOptions{Tokenizer: tokenizer, GSEDictionary: dictionary}, nil
}

func fileToolsEnabled(enabled map[string]bool) bool {
	for _, name := range []string{"file_search", "file_get_info", "file_read_preview"} {
		if enabled[name] {
			return true
		}
	}
	return false
}
