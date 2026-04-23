package eino

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestLocalServiceRuntimeExecuteSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"output":"ok"}`))
	}))
	defer server.Close()

	runtime := NewLocalServiceRuntime(server.URL, "local-model", time.Second)
	result, err := runtime.Execute(context.Background(), "hello")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !result.Success || result.Output != "ok" {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestLocalServiceRuntimeExecuteServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer server.Close()

	runtime := NewLocalServiceRuntime(server.URL, "local-model", time.Second)
	if _, err := runtime.Execute(context.Background(), "hello"); err == nil {
		t.Fatal("expected error")
	}
}

func TestLocalServiceRuntimeExecuteInvalidJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`not-json`))
	}))
	defer server.Close()

	runtime := NewLocalServiceRuntime(server.URL, "local-model", time.Second)
	if _, err := runtime.Execute(context.Background(), "hello"); err == nil {
		t.Fatal("expected error")
	}
}
