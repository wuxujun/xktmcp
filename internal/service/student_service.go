package service

import (
	"context"
	"errors"
	"strings"

	"github.com/wuxujun/xktmcp/internal/client"
	"github.com/wuxujun/xktmcp/internal/model"
)

var (
	ErrInvalidQuery = errors.New("query must not be empty")
	ErrInvalidID    = errors.New("id must not be empty")
)

type StudentService struct {
	api *client.StudentAPI
}

func NewStudentService(api *client.StudentAPI) *StudentService {
	return &StudentService{api: api}
}

func (s *StudentService) Search(ctx context.Context, query string) ([]model.Student, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, ErrInvalidQuery
	}
	return s.api.SearchStudents(ctx, query)
}

func (s *StudentService) SearchOrders(ctx context.Context, query string) ([]model.StudentOrder, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, ErrInvalidQuery
	}
	return s.api.SearchOrders(ctx, query)
}

func (s *StudentService) SearchExam(ctx context.Context, query string) ([]model.StudentExam, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, ErrInvalidQuery
	}
	return s.api.SearchExam(ctx, query)
}

func (s *StudentService) Get(ctx context.Context, id string) (*model.Student, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, ErrInvalidID
	}
	return s.api.GetStudent(ctx, id)
}
