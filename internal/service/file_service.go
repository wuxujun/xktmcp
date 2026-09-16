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
	"unicode/utf8"

	"github.com/wuxujun/xktmcp/internal/model"
)

const maxSearchFileBytes = 2 * 1024 * 1024

var (
	ErrInvalidFileSearchScope = errors.New("search_in must be all, title or content")
	ErrFileSearchQueryTooLong = errors.New("query must not exceed 256 characters")
)

// FileService searches a shared, administrator-configured directory on demand.
// No caller-supplied value is used to select or construct the root directory.
type FileService struct {
	root string
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
	defer root.Close()
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
	defer root.Close()
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
	defer root.Close()
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
	defer root.Close()
	linkInfo, err := root.Lstat(name)
	if err != nil || linkInfo.Mode()&os.ModeSymlink != 0 {
		return model.FilePreview{}, errors.New("path is not a regular file")
	}
	file, err := root.Open(name)
	if err != nil {
		return model.FilePreview{}, fmt.Errorf("read file preview: %w", err)
	}
	defer file.Close()
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
	return &FileService{root: abs}, nil
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
	root, err := os.OpenRoot(s.root)
	if err != nil {
		return nil, fmt.Errorf("open file search directory: %w", err)
	}
	defer root.Close()

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
	defer file.Close()
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
	const maxRunes = 240
	runes := []rune(content)
	start := 0
	if index := strings.Index(lowerContent, query); index >= 0 {
		start = max(0, utf8.RuneCountInString(lowerContent[:index])-60)
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
