package liverecovery

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const requestJSON = `{"model":"fixture","stream":false,"messages":[{"role":"tool","content":"temporary failure"}]}`
const responseJSON = `{"model":"fixture","message":{"role":"assistant","content":"recovered"},"done":true,"eval_count":3}`

func recoveryFor(t *testing.T, url string) *Recovery {
	t.Helper()
	r, err := New(Options{Endpoint: url, Origin: "http://model.test", Model: "fixture", OutputDir: filepath.Join(t.TempDir(), "branch"), MaxRequests: 1, MaxTokens: 10, Timeout: time.Second, CaptureSensitiveData: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	return r
}

func TestOllamaExplicitOriginBudgetsAndProvenance(t *testing.T) {
	var calls atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/api/chat" || r.Header.Get("Authorization") != "" {
			t.Error("unexpected request")
		}
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if body["options"].(map[string]any)["num_predict"] != float64(10) {
			t.Error("generation budget not passed")
		}
		fmt.Fprint(w, responseJSON)
	}))
	defer s.Close()
	r := recoveryFor(t, s.URL)
	if r.Active() || r.Matches(httptest.NewRequest("POST", "http://tool.test/api/chat", nil)) {
		t.Fatal("live scope too broad")
	}
	if !r.Matches(httptest.NewRequest("POST", "http://model.test/api/chat", nil)) {
		t.Fatal("model origin not matched")
	}
	_, _, body, err := r.Respond(context.Background(), []byte(requestJSON))
	if err != nil || string(body) != responseJSON {
		t.Fatal(string(body), err)
	}
	state := r.Snapshot()
	if !state.Hybrid || state.Deterministic || state.Requests != 1 || state.OutputTokens != 3 || len(state.ConfigurationHashes) != 1 {
		t.Fatal(state)
	}
	if _, _, _, err = r.Respond(context.Background(), []byte(requestJSON)); err == nil || calls.Load() != 1 {
		t.Fatal("request budget failed")
	}
}

func TestOllamaRejectsBadProviderResponses(t *testing.T) {
	for name, body := range map[string]string{
		"malformed": "{", "incomplete": `{"model":"fixture","message":{},"done":false,"eval_count":1}`,
		"missing-usage":  `{"model":"fixture","message":{},"done":true}`,
		"negative-usage": `{"model":"fixture","message":{},"done":true,"eval_count":-1}`,
		"over-budget":    `{"model":"fixture","message":{},"done":true,"eval_count":11}`,
		"wrong-model":    strings.Replace(responseJSON, "fixture", "different", 1),
		"error":          `{"error":"secret provider failure"}`,
		"oversized":      strings.Repeat("x", MaximumBody+1),
	} {
		t.Run(name, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) }))
			defer s.Close()
			r := recoveryFor(t, s.URL)
			if _, _, _, err := r.Respond(context.Background(), []byte(requestJSON)); err == nil {
				t.Fatal("bad response accepted")
			}
			if strings.Contains(r.Snapshot().Failure, "secret provider") {
				t.Fatal("provider error body leaked")
			}
		})
	}
}

func TestOllamaCancellationAndRedirect(t *testing.T) {
	for _, mode := range []string{"cancel", "timeout", "redirect", "http-error"} {
		t.Run(mode, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch mode {
				case "cancel", "timeout":
					io.Copy(io.Discard, r.Body)
					select {
					case <-r.Context().Done():
					case <-time.After(100 * time.Millisecond):
					}
				case "redirect":
					http.Redirect(w, r, "http://example.com", 302)
				case "http-error":
					http.Error(w, "secret", 500)
				}
			}))
			defer s.Close()
			r := recoveryFor(t, s.URL)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "cancel" {
				cancel()
			}
			if mode == "timeout" {
				r.options.Timeout = 5 * time.Millisecond
			}
			if _, _, _, err := r.Respond(ctx, []byte(requestJSON)); err == nil {
				t.Fatal("provider error accepted")
			}
		})
	}
}

func TestOllamaNDJSONCompletion(t *testing.T) {
	stream := `{"model":"fixture","message":{"role":"assistant","tool_calls":[{"function":{"name":"lookup","arguments":{"item":"good"}}}]},"done":false}` + "\n" + `{"model":"fixture","message":{"role":"assistant","content":""},"done":true,"eval_count":4}` + "\n"
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, stream) }))
	defer s.Close()
	r := recoveryFor(t, s.URL)
	_, headers, _, err := r.Respond(context.Background(), []byte(strings.Replace(requestJSON, `"stream":false`, `"stream":true`, 1)))
	if err != nil || headers.Get("Content-Type") != "application/x-ndjson" {
		t.Fatal(err, headers)
	}
	if _, err = validateResponse([]byte(strings.Split(stream, "\n")[0]), true, "fixture"); err == nil {
		t.Fatal("truncated stream accepted")
	}
}

func TestOllamaRejectsUnboundedOrRemoteConfiguration(t *testing.T) {
	for _, endpoint := range []string{"https://127.0.0.1:11434", "http://example.com", "http://localhost:11434", "http://127.0.0.1:11434/x", "http://user:secret@127.0.0.1:11434"} {
		if _, err := New(Options{Endpoint: endpoint}); err == nil {
			t.Fatal("invalid endpoint accepted")
		}
	}
}
