package model

import "time"

// WikiPage 表示一篇完整的 Wiki 词条/页面
type WikiPage struct {
	PageID    string    `json:"page_id"`
	Title     string    `json:"title"`
	Content   string    `json:"content"`
	Category  string    `json:"category"`
	Summary   string    `json:"summary"`
	Version   int       `json:"version"`
	Author    string    `json:"author,omitempty"`
	CreatedAt time.Time `json:"created_at,omitempty"`
	UpdatedAt time.Time `json:"updated_at,omitempty"`
}

// WikiSearchResult 表示 Wiki 词条检索的一条命中结果
type WikiSearchResult struct {
	PageID    string  `json:"page_id"`
	Title     string  `json:"title"`
	Summary   string  `json:"summary"`
	Category  string  `json:"category"`
	Score     float32 `json:"score,omitempty"`
	UpdatedAt string  `json:"updated_at,omitempty"`
}

// WikiNode 表示 Wiki 目录树结构的一个节点
type WikiNode struct {
	ID          string     `json:"id"`
	Title       string     `json:"title"`
	Category    string     `json:"category,omitempty"`
	HasChildren bool       `json:"has_children"`
	Children    []WikiNode `json:"children,omitempty"`
}

// WikiUpsertResult 表示新增/更新 Wiki 词条的执行结果
type WikiUpsertResult struct {
	PageID  string `json:"page_id"`
	Version int    `json:"version"`
	Status  string `json:"status"` // "created" | "updated" | "appended"
}

// WikiBacklink 表示引用了目标 Wiki 词条的关联反向链接项
type WikiBacklink struct {
	SourcePageID string `json:"source_page_id"`
	SourceTitle  string `json:"source_title"`
	Context      string `json:"context,omitempty"`
}
