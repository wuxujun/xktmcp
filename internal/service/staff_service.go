package service

import (
	"context"
	"strings"

	"github.com/wuxujun/xktmcp/internal/client"
	"github.com/wuxujun/xktmcp/internal/model"
)

type StaffService struct {
	api *client.StaffAPI
}

func NewStaffService(api *client.StaffAPI) *StaffService {
	return &StaffService{api: api}
}

func (s *StaffService) StaffSearch(ctx context.Context, userId, query string) ([]model.Staff, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, ErrInvalidQuery
	}
	return s.api.SearchStaffs(ctx, userId, query)
}
