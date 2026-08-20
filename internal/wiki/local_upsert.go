package wiki

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/wuxujun/xktmcp/internal/model"
)

// UpsertPage 仅在 local.write_dir 内创建或修改正式 Markdown 页面。
func (s *LocalSearcher) UpsertPage(ctx context.Context, userID, title, content, category, summary, mode string) (*model.WikiUpsertResult, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "" {
		mode = "create"
	}
	if mode != "create" && mode != "update" && mode != "append" {
		return nil, fmt.Errorf("invalid local wiki upsert mode %q", mode)
	}
	if err := s.refresh(ctx, true); err != nil {
		return nil, err
	}

	writeRoot, err := s.secureWriteRoot()
	if err != nil {
		return nil, err
	}
	existing, findErr := s.findDocument("", title)
	if mode == "create" && findErr == nil {
		return nil, fmt.Errorf("local wiki page %q already exists as %q", title, existing.result.PageID)
	}
	if mode == "create" && findErr != nil && !errors.Is(findErr, errLocalPageNotFound) {
		return nil, findErr
	}
	if mode != "create" && findErr != nil {
		return nil, findErr
	}

	path := filepath.Join(writeRoot, slugify(title)+".md")
	status := "created"
	version := 1
	created := time.Now().UTC().Format(time.RFC3339)
	frontmatter := map[string]string{}
	body := strings.TrimSpace(content)
	fileMode := os.FileMode(0o644)
	if mode != "create" {
		if !pathWithin(writeRoot, existing.path) {
			return nil, fmt.Errorf("local wiki page %q is outside write_dir %q", existing.result.PageID, s.cfg.WriteDir)
		}
		path = existing.path
		frontmatter = existing.frontmatter
		version, _ = strconv.Atoi(frontmatter["version"])
		version++
		if version < 1 {
			version = 1
		}
		if frontmatter["created"] != "" {
			created = frontmatter["created"]
		}
		if mode == "append" {
			body = strings.TrimSpace(existing.content) + "\n\n" + body
			status = "appended"
		} else {
			status = "updated"
		}
		if info, statErr := os.Stat(path); statErr == nil {
			fileMode = info.Mode().Perm()
		}
	} else if _, statErr := os.Stat(path); statErr == nil {
		return nil, fmt.Errorf("local wiki target path %q already exists", path)
	} else if !os.IsNotExist(statErr) {
		return nil, statErr
	}

	if strings.TrimSpace(category) == "" {
		category = frontmatter["category"]
	}
	if strings.TrimSpace(category) == "" {
		category = s.cfg.DefaultCategory
	}
	if strings.TrimSpace(summary) == "" {
		summary = summarize(body, 240)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	pageID := relativePageID(s.cfg.Root, path)
	updates := []frontmatterUpdate{
		{key: "page_id", value: pageID},
		{key: "title", value: title},
		{key: "category", value: strings.TrimSpace(category)},
		{key: "summary", value: strings.TrimSpace(summary)},
		{key: "version", value: strconv.Itoa(version)},
		{key: "created", value: created},
		{key: "updated", value: now},
	}
	if strings.TrimSpace(userID) != "" {
		updates = append(updates, frontmatterUpdate{key: "author", value: strings.TrimSpace(userID)})
	}
	raw := ""
	if mode != "create" {
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil, readErr
		}
		raw = string(data)
	}
	output := updateMarkdown(raw, updates, body)
	if err := atomicWriteFile(path, []byte(output), fileMode); err != nil {
		return nil, fmt.Errorf("write local wiki page %q: %w", path, err)
	}
	refreshErr := s.refresh(ctx, true)
	logErr := s.appendActivityLog(now, status, pageID, title, userID)
	if refreshErr != nil {
		return nil, refreshErr
	}
	if logErr != nil {
		return nil, logErr
	}
	return &model.WikiUpsertResult{PageID: pageID, Version: version, Status: status}, nil
}

func (s *LocalSearcher) secureWriteRoot() (string, error) {
	writeRoot := filepath.Join(s.cfg.Root, s.cfg.WriteDir)
	realRoot, err := filepath.EvalSymlinks(s.cfg.Root)
	if err != nil {
		return "", err
	}
	existingAncestor := writeRoot
	for {
		if _, statErr := os.Lstat(existingAncestor); statErr == nil {
			break
		} else if !os.IsNotExist(statErr) {
			return "", statErr
		}
		parent := filepath.Dir(existingAncestor)
		if parent == existingAncestor {
			return "", fmt.Errorf("cannot resolve local wiki write_dir ancestor %q", writeRoot)
		}
		existingAncestor = parent
	}
	realAncestor, err := filepath.EvalSymlinks(existingAncestor)
	if err != nil {
		return "", err
	}
	if !pathWithin(realRoot, realAncestor) {
		return "", fmt.Errorf("local wiki write_dir %q resolves outside local.root", s.cfg.WriteDir)
	}
	if err := os.MkdirAll(writeRoot, 0o755); err != nil {
		return "", fmt.Errorf("create local wiki write_dir %q: %w", writeRoot, err)
	}
	realWriteRoot, err := filepath.EvalSymlinks(writeRoot)
	if err != nil {
		return "", err
	}
	if !pathWithin(realRoot, realWriteRoot) {
		return "", fmt.Errorf("local wiki write_dir %q resolves outside local.root", s.cfg.WriteDir)
	}
	return writeRoot, nil
}

func pathWithin(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func relativePageID(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return strings.TrimSuffix(filepath.ToSlash(path), filepath.Ext(path))
	}
	return strings.TrimSuffix(filepath.ToSlash(rel), filepath.Ext(rel))
}

func slugify(title string) string {
	var builder strings.Builder
	dash := false
	for _, r := range strings.ToLower(strings.TrimSpace(title)) {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			builder.WriteRune(r)
			dash = false
		} else if builder.Len() > 0 && !dash {
			builder.WriteByte('-')
			dash = true
		}
	}
	slug := strings.Trim(builder.String(), "-")
	if slug == "" {
		hash := sha256.Sum256([]byte(title))
		slug = fmt.Sprintf("page-%x", hash[:6])
	}
	return slug
}

type frontmatterUpdate struct {
	key   string
	value string
}

func updateMarkdown(raw string, updates []frontmatterUpdate, body string) string {
	lines := strings.Split(strings.TrimPrefix(raw, "\ufeff"), "\n")
	frontmatterLines := make([]string, 0)
	if len(lines) > 0 && strings.TrimSpace(lines[0]) == "---" {
		for i := 1; i < len(lines); i++ {
			if strings.TrimSpace(lines[i]) == "---" {
				frontmatterLines = append(frontmatterLines, lines[1:i]...)
				break
			}
		}
	}
	positions := make(map[string]int)
	for i, line := range frontmatterLines {
		if len(line) > 0 && unicode.IsSpace(rune(line[0])) {
			continue
		}
		key, _, ok := strings.Cut(line, ":")
		if ok {
			positions[strings.ToLower(strings.TrimSpace(key))] = i
		}
	}
	for _, update := range updates {
		line := update.key + ": " + yamlScalar(update.value)
		if position, ok := positions[update.key]; ok {
			frontmatterLines[position] = line
		} else {
			positions[update.key] = len(frontmatterLines)
			frontmatterLines = append(frontmatterLines, line)
		}
	}
	return "---\n" + strings.Join(frontmatterLines, "\n") + "\n---\n\n" + strings.TrimSpace(body) + "\n"
}

func yamlScalar(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func atomicWriteFile(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	temp, err := os.CreateTemp(dir, ".wiki-upsert-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err = temp.Chmod(mode); err == nil {
		_, err = temp.Write(data)
	}
	if err == nil {
		err = temp.Sync()
	}
	closeErr := temp.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(tempPath, path)
}

func (s *LocalSearcher) appendActivityLog(timestamp, status, pageID, title, userID string) error {
	path := filepath.Join(s.cfg.Root, "log.md")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("append local wiki activity log: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	var entry strings.Builder
	if info.Size() == 0 {
		entry.WriteString("# Wiki Activity Log\n\n")
	}
	date := strings.SplitN(timestamp, "T", 2)[0]
	safeTitle := strings.NewReplacer("\n", " ", "|", "-").Replace(title)
	fmt.Fprintf(&entry, "## [%s] upsert | %s\n\n- status: %s\n- page_id: %s\n- actor: %s\n- timestamp: %s\n\n",
		date, safeTitle, status, pageID, strings.ReplaceAll(userID, "\n", " "), timestamp)
	if _, err := file.WriteString(entry.String()); err != nil {
		return fmt.Errorf("append local wiki activity log: %w", err)
	}
	return file.Sync()
}
