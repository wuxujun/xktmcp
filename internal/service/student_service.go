package service

import (
	"context"
	"errors"
	"strings"

	"github.com/wuxujun/xktmcp/internal/client"
	"github.com/wuxujun/xktmcp/internal/model"
)

var (
	ErrInvalidQuery  = errors.New("query must not be empty")
	ErrInvalidID     = errors.New("id must not be empty")
	ErrMissingUserID = errors.New("userId must not be empty")
)

type StudentService struct {
	api *client.StudentAPI
}

func NewStudentService(api *client.StudentAPI) *StudentService {
	return &StudentService{api: api}
}

func (s *StudentService) Search(ctx context.Context, userId, query string, page, pageSize int) ([]model.Student, error) {
	userId = strings.TrimSpace(userId)
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, ErrInvalidQuery
	}
	return s.api.SearchStudents(ctx, userId, query, page, pageSize)
}

func (s *StudentService) SearchOrders(ctx context.Context, userId, query string) ([]model.StudentOrder, error) {
	userId = strings.TrimSpace(userId)
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, ErrInvalidQuery
	}
	return s.api.SearchOrders(ctx, userId, query)
}

func (s *StudentService) SearchExam(ctx context.Context, userId, query string) ([]model.StudentExam, error) {
	userId = strings.TrimSpace(userId)
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, ErrInvalidQuery
	}
	return s.api.SearchExam(ctx, userId, query)
}

func (s *StudentService) Get(ctx context.Context, userId, id string) (*model.Student, error) {
	userId = strings.TrimSpace(userId)
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, ErrInvalidID
	}
	return s.api.GetStudent(ctx, userId, id)
}
