package model

type Rag struct {
	Title   string  `json:"title"`
	Content string  `json:"content"`
	Score   float32 `json:"score"`
	Url     string  `json:"url"`
}
