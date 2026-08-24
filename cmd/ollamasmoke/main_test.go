package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRunRecognizesOllamaToolCall(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"message":{"role":"assistant","tool_calls":[{"function":{"name":"policy_check","arguments":{"payment_id":"pay_1"}}}]},"done":true}`))
	}))
	defer server.Close()
	if err := run(server.URL, "test-model", time.Second, server.Client()); err != nil {
		t.Fatal(err)
	}
}

func TestRunRejectsPlainTextAnswer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"message":{"role":"assistant","content":"yes"},"done":true}`))
	}))
	defer server.Close()
	if err := run(server.URL, "test-model", time.Second, server.Client()); err == nil {
		t.Fatal("expected missing tool call to fail")
	}
}
