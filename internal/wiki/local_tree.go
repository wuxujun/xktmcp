package wiki

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/wuxujun/xktmcp/internal/model"
)

// ListTree 从配置的正式内容目录构建树，不读取 raw 或派生 _index.md。
func (s *LocalSearcher) ListTree(ctx context.Context, _ string, parentID string, depth int) ([]model.WikiNode, error) {
	if depth <= 0 {
		depth = 3
	}
	if parentID != "" {
		parent, contentRoot, err := s.resolveTreeParent(parentID)
		if err != nil {
			return nil, err
		}
		return s.readTreeChildren(ctx, contentRoot, parent, depth)
	}

	var nodes []model.WikiNode
	for _, contentDir := range s.cfg.ContentDirs {
		contentRoot := filepath.Join(s.cfg.Root, contentDir)
		children, err := s.readTreeChildren(ctx, contentRoot, contentRoot, depth)
		if err != nil {
			return nil, err
		}
		nodes = append(nodes, children...)
	}
	sortTreeNodes(nodes)
	return nodes, nil
}

func (s *LocalSearcher) readTreeChildren(ctx context.Context, contentRoot, parent string, depth int) ([]model.WikiNode, error) {
	entries, err := os.ReadDir(parent)
	if err != nil {
		return nil, fmt.Errorf("read wiki tree directory %q: %w", parent, err)
	}
	nodes := make([]model.WikiNode, 0, len(entries))
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if strings.HasPrefix(entry.Name(), ".") || entry.Type()&os.ModeSymlink != 0 || entry.Name() == "_index.md" {
			continue
		}
		path := filepath.Join(parent, entry.Name())
		if entry.IsDir() {
			hasChildren, err := s.directoryHasVisibleChildren(path)
			if err != nil {
				return nil, err
			}
			node := model.WikiNode{
				ID:          s.relativeID(path),
				Title:       entry.Name(),
				Category:    relativeCategory(contentRoot, path),
				HasChildren: hasChildren,
			}
			if depth > 1 && hasChildren {
				node.Children, err = s.readTreeChildren(ctx, contentRoot, path, depth-1)
				if err != nil {
					return nil, err
				}
			}
			nodes = append(nodes, node)
			continue
		}
		if !strings.EqualFold(filepath.Ext(entry.Name()), ".md") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return nil, err
		}
		if info.Size() > s.cfg.MaxFileSizeBytes {
			continue
		}
		doc, err := loadDocument(s.cfg.Root, contentRoot, path, info)
		if err != nil {
			return nil, err
		}
		nodes = append(nodes, model.WikiNode{
			ID:       doc.result.PageID,
			Title:    doc.result.Title,
			Category: doc.result.Category,
		})
	}
	sortTreeNodes(nodes)
	return nodes, nil
}

func (s *LocalSearcher) resolveTreeParent(parentID string) (string, string, error) {
	clean := filepath.Clean(filepath.FromSlash(strings.TrimSpace(parentID)))
	if clean == "." || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", "", fmt.Errorf("invalid local wiki parent_id %q", parentID)
	}
	candidate := filepath.Join(s.cfg.Root, clean)
	for _, contentDir := range s.cfg.ContentDirs {
		contentRoot := filepath.Join(s.cfg.Root, contentDir)
		realContentRoot, err := filepath.EvalSymlinks(contentRoot)
		if err != nil {
			return "", "", fmt.Errorf("resolve local wiki content directory %q: %w", contentRoot, err)
		}
		realCandidate, err := filepath.EvalSymlinks(candidate)
		if err != nil {
			return "", "", fmt.Errorf("stat local wiki parent_id %q: %w", parentID, err)
		}
		rel, err := filepath.Rel(realContentRoot, realCandidate)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		info, err := os.Stat(candidate)
		if err != nil {
			return "", "", fmt.Errorf("stat local wiki parent_id %q: %w", parentID, err)
		}
		if !info.IsDir() {
			return "", "", fmt.Errorf("local wiki parent_id %q is not a directory", parentID)
		}
		return candidate, contentRoot, nil
	}
	return "", "", fmt.Errorf("local wiki parent_id %q is outside configured content_dirs", parentID)
}

func (s *LocalSearcher) directoryHasVisibleChildren(path string) (bool, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".") || entry.Type()&os.ModeSymlink != 0 || entry.Name() == "_index.md" {
			continue
		}
		if entry.IsDir() || strings.EqualFold(filepath.Ext(entry.Name()), ".md") {
			return true, nil
		}
	}
	return false, nil
}

func (s *LocalSearcher) relativeID(path string) string {
	rel, err := filepath.Rel(s.cfg.Root, path)
	if err != nil {
		return filepath.ToSlash(path)
	}
	return filepath.ToSlash(rel)
}

func relativeCategory(contentRoot, path string) string {
	rel, err := filepath.Rel(contentRoot, path)
	if err != nil || rel == "." {
		return ""
	}
	return filepath.ToSlash(rel)
}

func sortTreeNodes(nodes []model.WikiNode) {
	sort.SliceStable(nodes, func(i, j int) bool {
		if nodes[i].HasChildren != nodes[j].HasChildren {
			return nodes[i].HasChildren
		}
		return strings.ToLower(nodes[i].Title) < strings.ToLower(nodes[j].Title)
	})
}
