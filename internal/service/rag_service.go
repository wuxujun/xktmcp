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

func (s *RagService) RagSearch(ctx context.Context, userId, query string) ([]model.Rag, error) {
	userId = strings.TrimSpace(userId)
	if userId == "" {
		return nil, ErrMissingUserID
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, ErrInvalidQuery
	}
	return s.api.SearchRags(ctx, userId, query)
}
