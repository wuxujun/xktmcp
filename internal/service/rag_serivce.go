package service

import (
	"context"
	"strings"

	"github.com/wuxujun/xktmcp/internal/client"
	"github.com/wuxujun/xktmcp/internal/model"
)

type RagService struct {
	api *client.RagAPI
}

func NewRagService(api *client.RagAPI) *RagService {
	return &RagService{api: api}
}

func (s *RagService) RagSearch(ctx context.Context, query string) ([]model.Rag, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, ErrInvalidQuery
	}
	return s.api.SearchRags(ctx, query)
}
