package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestStudentClientReadEndpoints(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer token" || r.URL.Query().Get("userId") != "u1" {
			t.Errorf("headers/query incorrect: auth=%q user=%q", r.Header.Get("Authorization"), r.URL.Query().Get("userId"))
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/student":
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"id": 1, "stu_name": "Alice"}}})
		case "/api/student/order":
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"id": 2}}})
		case "/api/student/exam":
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"id": 3}}})
		case "/api/student/1":
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"id": 1, "stu_name": "Alice"}}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	a := NewStudentAPI(Config{BaseURL: server.URL, APIToken: "token", Timeout: time.Second})
	ctx := context.Background()
	students, err := a.SearchStudents(ctx, "u1", "Alice", 1, 10)
	if err != nil || len(students) != 1 || students[0].StuName != "Alice" {
		t.Fatalf("students=%#v err=%v", students, err)
	}
	orders, err := a.SearchOrders(ctx, "u1", "Alice")
	if err != nil || len(orders) != 1 {
		t.Fatalf("orders=%#v err=%v", orders, err)
	}
	exams, err := a.SearchExam(ctx, "u1", "Alice")
	if err != nil || len(exams) != 1 {
		t.Fatalf("exams=%#v err=%v", exams, err)
	}
	student, err := a.GetStudent(ctx, "u1", "1")
	if err != nil || student == nil || student.StuName != "Alice" {
		t.Fatalf("student=%#v err=%v", student, err)
	}
}
