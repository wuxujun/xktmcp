package service

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/go-ego/gse"
	"github.com/wuxujun/xktmcp/internal/model"
)

const maxSearchFileBytes = 2 * 1024 * 1024

const (
	FileSearchTokenizerBuiltin = "builtin"
	FileSearchTokenizerGSE     = "gse"
	FileSearchGSEDictionaryZH  = "zh"
	FileSearchGSEDictionaryZHS = "zh_s"
)

var (
	ErrInvalidFileSearchScope = errors.New("search_in must be all, title or content")
	ErrFileSearchQueryTooLong = errors.New("query must not exceed 256 characters")
)

// FileSearchOptions controls the matching strategy used by file_search.
// The zero value preserves the original continuous-text matching behavior.
type FileSearchOptions struct {
	Tokenizer     string
	GSEDictionary string
}

type fileSearchTokenizer interface {
	Terms(string) []string
	ContentTerms(string) []string
}

type fileSearchDocument struct {
	name         string
	title        string
	content      string
	lowerName    string
	lowerTitle   string
	lowerContent string
	size         int64
	modTime      int64
	metadata     string
	searchable   bool
	nameTerms    []string
	titleTerms   []string
	contentTerms []string
}

type fileSearchIndex struct {
	files     map[string]fileSearchDocument
	paths     []string
	termIndex map[string][]string
}

// FileService searches a shared, administrator-configured directory on demand.
// No caller-supplied value is used to select or construct the root directory.
type FileService struct {
	root      string
	tokenizer fileSearchTokenizer
	indexMu   chan struct{}
	index     *fileSearchIndex
}

func (s *FileService) SearchFileNames(ctx context.Context, query string, useRegex bool, limit int) ([]model.FileNameSearchResult, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, ErrInvalidQuery
	}
	if utf8.RuneCountInString(query) > 256 {
		return nil, ErrFileSearchQueryTooLong
	}
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	var re *regexp.Regexp
	var err error
	if useRegex {
		re, err = regexp.Compile(query)
		if err != nil {
			return nil, fmt.Errorf("invalid filename regex: %w", err)
		}
	}
	root, err := os.OpenRoot(s.root)
	if err != nil {
		return nil, fmt.Errorf("open file search directory: %w", err)
	}
	defer func() { _ = root.Close() }()
	items := make([]model.FileNameSearchResult, 0, limit)
	err = fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			return walkErr
		}
		if name != "." && strings.HasPrefix(entry.Name(), ".") {
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if entry.IsDir() || !entry.Type().IsRegular() {
			return nil
		}
		matched := re != nil && re.MatchString(entry.Name()) || re == nil && strings.Contains(strings.ToLower(entry.Name()), strings.ToLower(query))
		if matched {
			items = append(items, model.FileNameSearchResult{Path: name, Name: entry.Name()})
			if len(items) >= limit {
				return errStopWalk
			}
		}
		return nil
	})
	if err != nil && !errors.Is(err, errStopWalk) {
		return nil, fmt.Errorf("search file names: %w", err)
	}
	return items, nil
}

func (s *FileService) SearchFileContent(ctx context.Context, query string, extensions []string, contextLines, limit int) ([]model.FileContentSearchResult, error) {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return nil, ErrInvalidQuery
	}
	if utf8.RuneCountInString(query) > 256 {
		return nil, ErrFileSearchQueryTooLong
	}
	if contextLines < 0 {
		return nil, errors.New("context_lines must be non-negative")
	}
	if contextLines > 10 {
		contextLines = 10
	}
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	ext := make(map[string]bool, len(extensions))
	for _, value := range extensions {
		value = strings.ToLower(strings.TrimSpace(value))
		if value != "" {
			if !strings.HasPrefix(value, ".") {
				value = "." + value
			}
			ext[value] = true
		}
	}
	root, err := os.OpenRoot(s.root)
	if err != nil {
		return nil, fmt.Errorf("open file search directory: %w", err)
	}
	defer func() { _ = root.Close() }()
	items := make([]model.FileContentSearchResult, 0, limit)
	err = fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			return walkErr
		}
		if name != "." && strings.HasPrefix(entry.Name(), ".") {
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if entry.IsDir() || !entry.Type().IsRegular() || !searchableTextExtension(path.Ext(name)) || (len(ext) > 0 && !ext[strings.ToLower(path.Ext(name))]) {
			return nil
		}
		data, err := readSearchFile(root, name)
		if err != nil {
			return err
		}
		if data == nil || !utf8.Valid(data) || strings.IndexByte(string(data), 0) >= 0 {
			return nil
		}
		lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
		for i, line := range lines {
			if !strings.Contains(strings.ToLower(line), query) {
				continue
			}
			start, end := max(0, i-contextLines), min(len(lines), i+contextLines+1)
			items = append(items, model.FileContentSearchResult{Path: name, Line: i + 1, Snippet: strings.TrimSpace(line), Context: strings.Join(lines[start:end], "\n")})
			if len(items) >= limit {
				return errStopWalk
			}
			break
		}
		return nil
	})
	if err != nil && !errors.Is(err, errStopWalk) {
		return nil, fmt.Errorf("search file content: %w", err)
	}
	return items, nil
}

func (s *FileService) GetFileInfo(ctx context.Context, filePath string) (model.FileInfo, error) {
	if err := ctx.Err(); err != nil {
		return model.FileInfo{}, err
	}
	name, err := cleanFilePath(filePath)
	if err != nil {
		return model.FileInfo{}, err
	}
	root, err := os.OpenRoot(s.root)
	if err != nil {
		return model.FileInfo{}, fmt.Errorf("open file search directory: %w", err)
	}
	defer func() { _ = root.Close() }()
	info, err := root.Lstat(name)
	if err != nil {
		return model.FileInfo{}, fmt.Errorf("stat file: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return model.FileInfo{}, errors.New("path is not a regular file")
	}
	ext := strings.ToLower(path.Ext(name))
	mimeType := mime.TypeByExtension(ext)
	if mimeType == "" && (ext == ".md" || ext == ".markdown") {
		mimeType = "text/markdown; charset=utf-8"
	}
	if mimeType == "" && searchableTextExtension(ext) {
		mimeType = "text/plain; charset=utf-8"
	}
	return model.FileInfo{Path: name, Name: path.Base(name), SizeBytes: info.Size(), ModifiedAt: info.ModTime().UTC().Format("2006-01-02T15:04:05Z07:00"), Extension: ext, MIMEType: mimeType}, nil
}

func (s *FileService) ReadFilePreview(ctx context.Context, filePath string, startLine, endLine int) (model.FilePreview, error) {
	if startLine < 1 || endLine < startLine {
		return model.FilePreview{}, errors.New("line range must start at 1 and end_line must be >= start_line")
	}
	if endLine-startLine >= 200 {
		return model.FilePreview{}, errors.New("line range must not exceed 200 lines")
	}
	name, err := cleanFilePath(filePath)
	if err != nil {
		return model.FilePreview{}, err
	}
	root, err := os.OpenRoot(s.root)
	if err != nil {
		return model.FilePreview{}, fmt.Errorf("open file search directory: %w", err)
	}
	defer func() { _ = root.Close() }()
	linkInfo, err := root.Lstat(name)
	if err != nil || linkInfo.Mode()&os.ModeSymlink != 0 {
		return model.FilePreview{}, errors.New("path is not a regular file")
	}
	file, err := root.Open(name)
	if err != nil {
		return model.FilePreview{}, fmt.Errorf("read file preview: %w", err)
	}
	defer func() { _ = file.Close() }()
	if info, err := file.Stat(); err != nil || !info.Mode().IsRegular() {
		return model.FilePreview{}, errors.New("file is not a regular file")
	}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	lines := make([]string, 0, endLine-startLine+1)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		if lineNo < startLine {
			continue
		}
		if lineNo > endLine {
			break
		}
		line := scanner.Text()
		if !utf8.ValidString(line) || strings.IndexByte(line, 0) >= 0 {
			return model.FilePreview{}, errors.New("file is not a readable UTF-8 text file")
		}
		lines = append(lines, line)
		if len([]byte(strings.Join(lines, "\n"))) > 256*1024 {
			return model.FilePreview{}, errors.New("preview exceeds 256 KiB")
		}
	}
	if err := scanner.Err(); err != nil {
		return model.FilePreview{}, fmt.Errorf("read file preview: %w", err)
	}
	if lineNo < startLine {
		return model.FilePreview{}, errors.New("start_line exceeds file length")
	}
	if endLine > lineNo {
		endLine = lineNo
	}
	if err := ctx.Err(); err != nil {
		return model.FilePreview{}, err
	}
	return model.FilePreview{Path: name, StartLine: startLine, EndLine: endLine, Content: strings.Join(lines, "\n")}, nil
}

var errStopWalk = errors.New("stop file walk")

func cleanFilePath(value string) (string, error) {
	value = filepath.ToSlash(strings.TrimSpace(value))
	if value == "" || path.IsAbs(value) || value == "." || strings.HasPrefix(value, "../") || strings.Contains(value, "/../") || strings.Contains(value, "\\") {
		return "", errors.New("path must be a relative file path within FILE_SEARCH_ROOT")
	}
	return value, nil
}

func NewFileService(root string) (*FileService, error) {
	return NewFileServiceWithOptions(root, FileSearchOptions{})
}

func NewFileServiceWithOptions(root string, options FileSearchOptions) (*FileService, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil, errors.New("file search root must not be empty")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve file search root: %w", err)
	}
	dir, err := os.OpenRoot(abs)
	if err != nil {
		return nil, fmt.Errorf("open file search root: %w", err)
	}
	if err := dir.Close(); err != nil {
		return nil, fmt.Errorf("close file search root: %w", err)
	}
	tokenizer, err := newFileSearchTokenizer(options.Tokenizer, options.GSEDictionary)
	if err != nil {
		return nil, err
	}
	return &FileService{root: abs, tokenizer: tokenizer, indexMu: make(chan struct{}, 1)}, nil
}

func (s *FileService) Search(ctx context.Context, query, searchIn string, limit int) ([]model.FileSearchResult, error) {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return nil, ErrInvalidQuery
	}
	if utf8.RuneCountInString(query) > 256 {
		return nil, ErrFileSearchQueryTooLong
	}
	searchIn = strings.ToLower(strings.TrimSpace(searchIn))
	if searchIn == "" {
		searchIn = "all"
	}
	if searchIn != "all" && searchIn != "title" && searchIn != "content" {
		return nil, ErrInvalidFileSearchScope
	}
	if limit <= 0 {
		limit = 20
	} else if limit > 100 {
		limit = 100
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.tokenizer != nil {
		return s.searchTokenized(ctx, query, searchIn, limit)
	}
	root, err := os.OpenRoot(s.root)
	if err != nil {
		return nil, fmt.Errorf("open file search directory: %w", err)
	}
	defer func() { _ = root.Close() }()

	items := make([]model.FileSearchResult, 0, limit)
	err = fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			return walkErr
		}
		if name != "." && strings.HasPrefix(entry.Name(), ".") {
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if entry.IsDir() || !entry.Type().IsRegular() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Size() > maxSearchFileBytes {
			return nil
		}
		title, content := entry.Name(), ""
		if searchableTextExtension(path.Ext(name)) {
			data, err := readSearchFile(root, name)
			if err != nil {
				return err
			}
			if data == nil || !utf8.Valid(data) || strings.IndexByte(string(data), 0) >= 0 {
				return nil
			}
			content = strings.TrimPrefix(string(data), "\ufeff")
			if ext := strings.ToLower(path.Ext(name)); ext == ".md" || ext == ".markdown" {
				title = fileMarkdownTitle(content, title)
			}
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		titleMatch := searchIn != "content" && (strings.Contains(strings.ToLower(title), query) || strings.Contains(strings.ToLower(entry.Name()), query))
		lowerContent := strings.ToLower(content)
		contentMatch := searchIn != "title" && strings.Contains(lowerContent, query)
		if !titleMatch && !contentMatch {
			return nil
		}
		fields := make([]string, 0, 2)
		if titleMatch {
			fields = append(fields, "title")
		}
		if contentMatch {
			fields = append(fields, "content")
		}
		item := model.FileSearchResult{
			Path: name, Title: title, Snippet: fileSearchSnippet(content, lowerContent, query),
			MatchedFields: fields, SizeBytes: info.Size(),
		}
		// Keep at most limit results, with title matches ahead of content matches.
		pos := sort.Search(len(items), func(i int) bool {
			otherTitleMatch := items[i].MatchedFields[0] == "title"
			if titleMatch != otherTitleMatch {
				return titleMatch
			}
			return item.Path < items[i].Path
		})
		if pos < limit {
			items = append(items, model.FileSearchResult{})
			copy(items[pos+1:], items[pos:])
			items[pos] = item
			if len(items) > limit {
				items = items[:limit]
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("search local files: %w", err)
	}
	return items, nil
}

func newFileSearchTokenizer(name, dictionary string) (fileSearchTokenizer, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		name = FileSearchTokenizerBuiltin
	}
	switch name {
	case FileSearchTokenizerBuiltin:
		return nil, nil
	case FileSearchTokenizerGSE:
		dictionary, err := normalizeFileSearchGSEDictionary(dictionary)
		if err != nil {
			return nil, err
		}
		segmenter, err := sharedFileGSESegmenter(dictionary)
		if err != nil {
			return nil, fmt.Errorf("load file search gse dictionary: %w", err)
		}
		return &fileGSESearchTokenizer{segmenter: segmenter}, nil
	default:
		return nil, fmt.Errorf("unsupported file search tokenizer %q (want %q or %q)", name, FileSearchTokenizerBuiltin, FileSearchTokenizerGSE)
	}
}

func normalizeFileSearchGSEDictionary(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return FileSearchGSEDictionaryZH, nil
	}
	if value != FileSearchGSEDictionaryZH && value != FileSearchGSEDictionaryZHS {
		return "", fmt.Errorf("unsupported file search gse dictionary %q (want %q or %q)", value, FileSearchGSEDictionaryZH, FileSearchGSEDictionaryZHS)
	}
	return value, nil
}

type fileGSESearchTokenizer struct {
	mu        sync.Mutex
	segmenter gse.Segmenter
}

type fileGSETemplateEntry struct {
	once      sync.Once
	segmenter gse.Segmenter
	err       error
}

var fileGSEModelOnce sync.Once

var fileGSETemplates = map[string]*fileGSETemplateEntry{
	FileSearchGSEDictionaryZH:  {},
	FileSearchGSEDictionaryZHS: {},
}

func sharedFileGSESegmenter(dictionary string) (gse.Segmenter, error) {
	entry := fileGSETemplates[dictionary]
	entry.once.Do(func() {
		fileGSEModelOnce.Do(func() { entry.segmenter.LoadModel() })
		entry.segmenter.NotLoadHMM = true
		entry.segmenter.SkipLog = true
		entry.err = entry.segmenter.LoadDictEmbed(dictionary)
	})
	if entry.err != nil {
		return gse.Segmenter{}, entry.err
	}
	return entry.segmenter, nil
}

func (t *fileGSESearchTokenizer) Terms(text string) []string {
	return t.terms(text, true)
}

func (t *fileGSESearchTokenizer) ContentTerms(text string) []string {
	return t.terms(text, false)
}

func (t *fileGSESearchTokenizer) terms(text string, unique bool) []string {
	text = strings.ToLower(strings.TrimSpace(text))
	if text == "" {
		return nil
	}

	t.mu.Lock()
	words := t.segmenter.CutSearch(text, true)
	t.mu.Unlock()

	terms := make([]string, 0, len(words)+4)
	var seen map[string]struct{}
	if unique {
		seen = make(map[string]struct{}, len(words)+4)
	}
	appendTerm := func(term string) {
		term = strings.ToLower(strings.TrimSpace(term))
		if term == "" || !isFileSearchTerm(term) {
			return
		}
		if unique {
			if _, ok := seen[term]; ok {
				return
			}
			seen[term] = struct{}{}
		}
		terms = append(terms, term)
	}
	for _, word := range words {
		appendTerm(word)
	}
	if len(terms) == 0 {
		for _, term := range fileSearchQueryTerms(text) {
			appendTerm(term)
		}
	}
	return terms
}

func isFileSearchTerm(term string) bool {
	for _, r := range term {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			return true
		}
	}
	return false
}

func fileSearchQueryTerms(query string) []string {
	var terms []string
	var current []rune
	currentHan := false
	flush := func() {
		if len(current) > 0 {
			terms = append(terms, string(current))
			current = current[:0]
		}
	}
	for _, r := range query {
		if unicode.IsSpace(r) || unicode.IsPunct(r) || unicode.IsSymbol(r) {
			flush()
			continue
		}
		isHan := unicode.Is(unicode.Han, r)
		if len(current) > 0 && isHan != currentHan {
			flush()
		}
		currentHan = isHan
		current = append(current, r)
	}
	flush()
	if len(terms) == 0 {
		return []string{query}
	}
	return terms
}

func (s *FileService) searchTokenized(ctx context.Context, query, searchIn string, limit int) ([]model.FileSearchResult, error) {
	index, err := s.ensureFileSearchIndex(ctx)
	if err != nil {
		return nil, err
	}
	terms := s.tokenizer.Terms(query)
	candidates := make(map[string]struct{})
	addTermCandidates := func(terms []string) {
		for _, term := range terms {
			for _, name := range index.termIndex[term] {
				candidates[name] = struct{}{}
			}
		}
	}
	if searchIn != "title" {
		addTermCandidates(terms)
	} else {
		for _, term := range terms {
			for _, name := range index.termIndex[term] {
				doc := index.files[name]
				if fileSearchTermIn(doc.nameTerms, term) || fileSearchTermIn(doc.titleTerms, term) {
					candidates[name] = struct{}{}
				}
			}
		}
	}
	for _, name := range index.paths {
		doc := index.files[name]
		if (searchIn != "content" && (strings.Contains(doc.lowerName, query) || strings.Contains(doc.lowerTitle, query))) ||
			(searchIn != "title" && strings.Contains(doc.lowerContent, query)) {
			candidates[name] = struct{}{}
		}
	}

	items := make([]model.FileSearchResult, 0, limit)
	for _, name := range index.paths {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if _, ok := candidates[name]; !ok {
			continue
		}
		doc := index.files[name]
		titleMatch, contentMatch := fileSearchDocumentMatches(doc, query, terms, searchIn)
		if !titleMatch && !contentMatch {
			continue
		}
		fields := make([]string, 0, 2)
		if titleMatch {
			fields = append(fields, "title")
		}
		if contentMatch {
			fields = append(fields, "content")
		}
		item := model.FileSearchResult{
			Path: name, Title: doc.title, Snippet: fileSearchSnippetWithTerms(doc.content, doc.lowerContent, query, terms),
			MatchedFields: fields, SizeBytes: doc.size,
		}
		pos := sort.Search(len(items), func(i int) bool {
			otherTitleMatch := items[i].MatchedFields[0] == "title"
			if titleMatch != otherTitleMatch {
				return titleMatch
			}
			return item.Path < items[i].Path
		})
		if pos < limit {
			items = append(items, model.FileSearchResult{})
			copy(items[pos+1:], items[pos:])
			items[pos] = item
			if len(items) > limit {
				items = items[:limit]
			}
		}
	}
	return items, nil
}

func (s *FileService) ensureFileSearchIndex(ctx context.Context) (*fileSearchIndex, error) {
	select {
	case s.indexMu <- struct{}{}:
		defer func() { <-s.indexMu }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	root, err := os.OpenRoot(s.root)
	if err != nil {
		return nil, fmt.Errorf("open file search directory: %w", err)
	}
	defer func() { _ = root.Close() }()

	previous := s.index
	files := make(map[string]fileSearchDocument)
	changed := previous == nil
	err = fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			return walkErr
		}
		if name != "." && strings.HasPrefix(entry.Name(), ".") {
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if entry.IsDir() || !entry.Type().IsRegular() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Size() > maxSearchFileBytes {
			return nil
		}
		if previous != nil {
			if old, ok := previous.files[name]; ok && old.size == info.Size() && old.modTime == info.ModTime().UnixNano() && old.metadata == fileSearchMetadata(info) {
				files[name] = old
				return nil
			}
		}
		doc, ok, err := loadFileSearchDocument(root, name, info, s.tokenizer)
		if err != nil {
			return err
		}
		changed = true
		if ok {
			files[name] = doc
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("index local files: %w", err)
	}
	if previous != nil && len(files) != len(previous.files) {
		changed = true
	}
	if !changed {
		return previous, nil
	}
	index := buildFileSearchIndex(files)
	s.index = index
	return index, nil
}

func loadFileSearchDocument(root *os.Root, name string, info fs.FileInfo, tokenizer fileSearchTokenizer) (fileSearchDocument, bool, error) {
	doc := fileSearchDocument{
		name:       info.Name(),
		title:      info.Name(),
		size:       info.Size(),
		modTime:    info.ModTime().UnixNano(),
		metadata:   fileSearchMetadata(info),
		searchable: true,
	}
	if searchableTextExtension(path.Ext(name)) {
		data, err := readSearchFile(root, name)
		if err != nil {
			return fileSearchDocument{}, false, err
		}
		if data == nil || !utf8.Valid(data) || strings.IndexByte(string(data), 0) >= 0 {
			doc.searchable = false
			return doc, true, nil
		}
		doc.content = strings.TrimPrefix(string(data), "\ufeff")
		if ext := strings.ToLower(path.Ext(name)); ext == ".md" || ext == ".markdown" {
			doc.title = fileMarkdownTitle(doc.content, doc.title)
		}
	}
	doc.lowerName = strings.ToLower(doc.name)
	doc.lowerTitle = strings.ToLower(doc.title)
	doc.lowerContent = strings.ToLower(doc.content)
	doc.nameTerms = tokenizer.Terms(doc.name)
	doc.titleTerms = tokenizer.Terms(doc.title)
	doc.contentTerms = tokenizer.ContentTerms(doc.content)
	sort.Strings(doc.nameTerms)
	sort.Strings(doc.titleTerms)
	sort.Strings(doc.contentTerms)
	return doc, true, nil
}

func fileSearchMetadata(info fs.FileInfo) string {
	return fmt.Sprintf("%#v", info.Sys())
}

func buildFileSearchIndex(files map[string]fileSearchDocument) *fileSearchIndex {
	paths := make([]string, 0, len(files))
	for name := range files {
		paths = append(paths, name)
	}
	sort.Strings(paths)
	index := &fileSearchIndex{files: files, paths: paths, termIndex: make(map[string][]string)}
	for _, name := range paths {
		doc := files[name]
		if !doc.searchable {
			continue
		}
		seen := make(map[string]struct{}, len(doc.nameTerms)+len(doc.titleTerms)+len(doc.contentTerms))
		addTerms := func(terms []string) {
			for _, term := range terms {
				if term == "" {
					continue
				}
				if _, ok := seen[term]; ok {
					continue
				}
				seen[term] = struct{}{}
				index.termIndex[term] = append(index.termIndex[term], name)
			}
		}
		addTerms(doc.nameTerms)
		addTerms(doc.titleTerms)
		addTerms(doc.contentTerms)
	}
	return index
}

func fileSearchDocumentMatches(doc fileSearchDocument, query string, terms []string, searchIn string) (bool, bool) {
	if !doc.searchable {
		return false, false
	}
	titleMatch := false
	if searchIn != "content" {
		titleMatch = strings.Contains(doc.lowerName, query) || strings.Contains(doc.lowerTitle, query) ||
			fileSearchHasAnyTerm(doc.nameTerms, terms) || fileSearchHasAnyTerm(doc.titleTerms, terms)
	}
	contentMatch := false
	if searchIn != "title" {
		contentMatch = strings.Contains(doc.lowerContent, query) || fileSearchHasAnyTerm(doc.contentTerms, terms)
	}
	return titleMatch, contentMatch
}

func fileSearchHasAnyTerm(terms, wants []string) bool {
	for _, want := range wants {
		if fileSearchTermIn(terms, want) {
			return true
		}
	}
	return false
}

func fileSearchTermIn(terms []string, want string) bool {
	index := sort.SearchStrings(terms, want)
	return index < len(terms) && terms[index] == want
}

func searchableTextExtension(ext string) bool {
	switch strings.ToLower(ext) {
	case ".md", ".markdown", ".txt", ".text", ".csv", ".json", ".yaml", ".yml", ".xml", ".html", ".htm":
		return true
	default:
		return false
	}
}

func readSearchFile(root *os.Root, name string) ([]byte, error) {
	file, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxSearchFileBytes {
		return nil, nil
	}
	data, err := io.ReadAll(io.LimitReader(file, maxSearchFileBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxSearchFileBytes {
		return nil, nil
	}
	return data, nil
}

func fileMarkdownTitle(content, fallback string) string {
	var fence string
	for line := range strings.Lines(content) {
		line = strings.TrimSpace(line)
		if fence != "" {
			if strings.HasPrefix(line, fence) {
				fence = ""
			}
			continue
		}
		if strings.HasPrefix(line, "```") || strings.HasPrefix(line, "~~~") {
			fence = line[:3]
			continue
		}
		if strings.HasPrefix(line, "# ") || strings.HasPrefix(line, "#\t") {
			title := strings.TrimSpace(strings.TrimLeft(line[1:], " \t"))
			if title != "" {
				return title
			}
		}
	}
	return fallback
}

func fileSearchSnippet(content, lowerContent, query string) string {
	return fileSearchSnippetAt(content, lowerContent, strings.Index(lowerContent, query))
}

func fileSearchSnippetWithTerms(content, lowerContent, query string, terms []string) string {
	index := strings.Index(lowerContent, query)
	if index < 0 {
		for _, term := range terms {
			termIndex := strings.Index(lowerContent, term)
			if termIndex >= 0 && (index < 0 || termIndex < index) {
				index = termIndex
			}
		}
	}
	return fileSearchSnippetAt(content, lowerContent, index)
}

func fileSearchSnippetAt(content, lowerContent string, matchIndex int) string {
	const maxRunes = 240
	runes := []rune(content)
	start := 0
	if matchIndex >= 0 {
		start = max(0, utf8.RuneCountInString(lowerContent[:matchIndex])-60)
	}
	end := min(len(runes), start+maxRunes)
	snippet := string(runes[start:end])
	if start > 0 {
		snippet = "…" + snippet
	}
	if end < len(runes) {
		snippet += "…"
	}
	return snippet
}
