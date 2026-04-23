package eino

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestLocalServiceRuntimeExecuteUnavailable(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	addr := listener.Addr().String()
	listener.Close()

	runtime := NewLocalServiceRuntime("http://"+addr, "local-model", time.Second)
	_, err = runtime.Execute(context.Background(), "hello")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "local runtime unavailable") {
		t.Fatalf("unexpected error: %v", err)
	}
}
