package model

// FileSearchResult describes a match below the configured file search directory.
type FileSearchResult struct {
	Path          string   `json:"path"`
	Title         string   `json:"title"`
	Snippet       string   `json:"snippet"`
	MatchedFields []string `json:"matched_fields"`
	SizeBytes     int64    `json:"size_bytes"`
}

type FileNameSearchResult struct {
	Path string `json:"path"`
	Name string `json:"name"`
}

type FileContentSearchResult struct {
	Path    string `json:"path"`
	Line    int    `json:"line"`
	Snippet string `json:"snippet"`
	Context string `json:"context,omitempty"`
}

type FileInfo struct {
	Path       string `json:"path"`
	Name       string `json:"name"`
	SizeBytes  int64  `json:"size_bytes"`
	ModifiedAt string `json:"modified_at"`
	Extension  string `json:"extension"`
	MIMEType   string `json:"mime_type"`
	IsDir      bool   `json:"is_dir"`
}

type FilePreview struct {
	Path      string `json:"path"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
	Content   string `json:"content"`
}
