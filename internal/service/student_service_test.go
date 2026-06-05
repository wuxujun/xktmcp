package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/wuxujun/xktmcp/internal/client"
	"github.com/wuxujun/xktmcp/internal/model"
)

func TestStudentServiceSearch(t *testing.T) {
	t.Run("empty query returns error", func(t *testing.T) {
		svc := NewStudentService(nil)
		_, err := svc.Search(context.Background(), "  ")
		if err != ErrInvalidQuery {
			t.Errorf("expected ErrInvalidQuery, got %v", err)
		}
	})

	t.Run("success returns students", func(t *testing.T) {
		mockStudents := []model.Student{
			{ID: 1, StuName: "张三", SmpId: "smp_1001"},
		}

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/api/student" {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			if r.URL.Query().Get("query") != "张三" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if r.Header.Get("Authorization") != "Bearer test-token" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}

			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"data": mockStudents,
			})
		}))
		defer server.Close()

		cfg := client.Config{
			BaseURL:  server.URL,
			APIToken: "test-token",
			Timeout:  2 * time.Second,
		}
		api := client.NewStudentAPI(cfg)
		svc := NewStudentService(api)

		res, err := svc.Search(context.Background(), "张三")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if len(res) != 1 || res[0].StuName != "张三" {
			t.Errorf("expected student 张三, got %+v", res)
		}
	})
}

func TestStudentServiceSearchOrders(t *testing.T) {
	t.Run("empty query returns error", func(t *testing.T) {
		svc := NewStudentService(nil)
		_, err := svc.SearchOrders(context.Background(), "")
		if err != ErrInvalidQuery {
			t.Errorf("expected ErrInvalidQuery, got %v", err)
		}
	})

	t.Run("success returns orders", func(t *testing.T) {
		mockOrders := []model.StudentOrder{
			{ID: 10, SmpId: "smp_1001"},
		}

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/api/student/order" {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"data": mockOrders,
			})
		}))
		defer server.Close()

		cfg := client.Config{
			BaseURL:  server.URL,
			APIToken: "test-token",
			Timeout:  2 * time.Second,
		}
		api := client.NewStudentAPI(cfg)
		svc := NewStudentService(api)

		res, err := svc.SearchOrders(context.Background(), "smp_1001")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if len(res) != 1 || res[0].ID != 10 {
			t.Errorf("expected order ID 10, got %+v", res)
		}
	})
}

func TestStudentServiceSearchExam(t *testing.T) {
	t.Run("empty query returns error", func(t *testing.T) {
		svc := NewStudentService(nil)
		_, err := svc.SearchExam(context.Background(), " ")
		if err != ErrInvalidQuery {
			t.Errorf("expected ErrInvalidQuery, got %v", err)
		}
	})

	t.Run("success returns exams", func(t *testing.T) {
		mockExams := []model.StudentExam{
			{ID: 20, SmpId: "smp_1001"},
		}

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/api/student/exam" {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"data": mockExams,
			})
		}))
		defer server.Close()

		cfg := client.Config{
			BaseURL:  server.URL,
			APIToken: "test-token",
			Timeout:  2 * time.Second,
		}
		api := client.NewStudentAPI(cfg)
		svc := NewStudentService(api)

		res, err := svc.SearchExam(context.Background(), "smp_1001")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if len(res) != 1 || res[0].ID != 20 {
			t.Errorf("expected exam ID 20, got %+v", res)
		}
	})
}

func TestStudentServiceGet(t *testing.T) {
	t.Run("empty ID returns error", func(t *testing.T) {
		svc := NewStudentService(nil)
		_, err := svc.Get(context.Background(), "")
		if err != ErrInvalidID {
			t.Errorf("expected ErrInvalidID, got %v", err)
		}
	})

	t.Run("success returns student", func(t *testing.T) {
		mockStudent := model.Student{ID: 1, StuName: "李四", SmpId: "smp_1002"}

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/api/student/smp_1002" {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"data": []model.Student{mockStudent},
			})
		}))
		defer server.Close()

		cfg := client.Config{
			BaseURL:  server.URL,
			APIToken: "test-token",
			Timeout:  2 * time.Second,
		}
		api := client.NewStudentAPI(cfg)
		svc := NewStudentService(api)

		res, err := svc.Get(context.Background(), "smp_1002")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if res.StuName != "李四" {
			t.Errorf("expected 李四, got %+v", res)
		}
	})
}
