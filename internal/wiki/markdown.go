package wiki

import (
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/wuxujun/xktmcp/internal/model"
)

func loadDocument(root, contentRoot, path string, info fs.FileInfo) (localDocument, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return localDocument{}, err
	}
	frontmatter, body := parseFrontmatter(string(data))
	relativePath, err := filepath.Rel(root, path)
	if err != nil {
		return localDocument{}, err
	}
	pageID := strings.TrimSuffix(filepath.ToSlash(relativePath), filepath.Ext(relativePath))
	if configuredID := frontmatter["page_id"]; configuredID != "" {
		pageID = configuredID
	}
	title := frontmatter["title"]
	if title == "" {
		title = firstHeading(body)
	}
	if title == "" {
		title = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	}
	category := frontmatter["category"]
	if category == "" {
		rel, relErr := filepath.Rel(contentRoot, filepath.Dir(path))
		if relErr == nil && rel != "." {
			category = filepath.ToSlash(rel)
		}
	}
	summary := frontmatter["summary"]
	if summary == "" {
		summary = summarize(body, 240)
	}
	updated := frontmatter["updated"]
	if updated == "" {
		updated = info.ModTime().UTC().Format("2006-01-02T15:04:05Z")
	}
	return localDocument{
		result: model.WikiSearchResult{
			PageID:    pageID,
			Title:     title,
			Summary:   summary,
			Category:  category,
			UpdatedAt: updated,
		},
		content:     body,
		path:        path,
		frontmatter: frontmatter,
	}, nil
}

func parseFrontmatter(content string) (map[string]string, string) {
	values := make(map[string]string)
	content = strings.TrimPrefix(content, "\ufeff")
	if !strings.HasPrefix(content, "---\n") && !strings.HasPrefix(content, "---\r\n") {
		return values, content
	}
	lines := strings.Split(content, "\n")
	closing := -1
	for i := 1; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if line == "---" {
			closing = i
			break
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		value = parseFrontmatterScalar(value)
		values[key] = value
	}
	if closing < 0 {
		return map[string]string{}, content
	}
	return values, strings.TrimSpace(strings.Join(lines[closing+1:], "\n"))
}

func parseFrontmatterScalar(value string) string {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "\"") && strings.HasSuffix(value, "\"") {
		if unquoted, err := strconv.Unquote(value); err == nil {
			return unquoted
		}
	}
	return strings.Trim(value, "\"'")
}

func firstHeading(body string) string {
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "# ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "# "))
		}
	}
	return ""
}

func summarize(body string, maxRunes int) string {
	paragraphs := strings.Split(body, "\n\n")
	for _, paragraph := range paragraphs {
		paragraph = strings.Join(strings.Fields(paragraph), " ")
		if paragraph == "" || strings.HasPrefix(paragraph, "#") {
			continue
		}
		if utf8.RuneCountInString(paragraph) <= maxRunes {
			return paragraph
		}
		runes := []rune(paragraph)
		return string(runes[:maxRunes]) + "…"
	}
	return ""
}
