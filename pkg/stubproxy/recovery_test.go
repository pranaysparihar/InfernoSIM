package stubproxy

import (
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"infernosim/pkg/event"
	"infernosim/pkg/liverecovery"
)

func TestRecoveryNeverRejoinsTapeOrOpensToolPassthrough(t *testing.T) {
	const known = `{"model":"fixture","stream":false,"messages":[{"role":"user","content":"recorded"}]}`
	const divergent = `{"model":"fixture","stream":false,"messages":[{"role":"user","content":"recovery"}]}`
	var modelCalls, toolCalls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		modelCalls.Add(1)
		io.Copy(io.Discard, r.Body)
		fmt.Fprint(w, `{"model":"fixture","message":{"role":"assistant","content":"live"},"done":true,"eval_count":1}`)
	}))
	defer provider.Close()
	tool := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { toolCalls.Add(1) }))
	defer tool.Close()
	live, err := liverecovery.New(liverecovery.Options{Endpoint: provider.URL, Origin: "http://model.test", Model: "fixture", OutputDir: filepath.Join(t.TempDir(), "branch"), MaxRequests: 2, MaxTokens: 8, Timeout: time.Second, CaptureSensitiveData: true})
	if err != nil {
		t.Fatal(err)
	}
	defer live.Close()
	path := writeOutboundFixture(t, event.Event{ID: "original", Type: "OutboundCall", Method: "POST", URL: "http://model.test/api/chat", BodyB64: base64.StdEncoding.EncodeToString([]byte(known)), BodySize: int64(len(known)), Status: 200, ResponseCaptured: true, ResponseBodyB64: base64.StdEncoding.EncodeToString([]byte(`{"recorded":true}`))})
	stub, err := NewWithOptions(path, "", nil, Options{Recovery: live})
	if err != nil {
		t.Fatal(err)
	}
	post := func(target, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		stub.ServeHTTP(w, httptest.NewRequest("POST", target, strings.NewReader(body)))
		return w
	}
	first := post("http://model.test/api/chat", known)
	if first.Code != 200 || !strings.Contains(first.Body.String(), "recorded") || modelCalls.Load() != 0 {
		t.Fatal("recorded prefix contacted model", first.Body.String())
	}
	for _, body := range []string{divergent, known} {
		w := post("http://model.test/api/chat", body)
		if w.Code != 200 || !strings.Contains(w.Body.String(), "live") {
			t.Fatal("divergent branch rejoined tape", w.Code, w.Body.String())
		}
	}
	w := post(tool.URL+"/mcp", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"unrecorded","arguments":{}}}`)
	if w.Code != http.StatusBadGateway || toolCalls.Load() != 0 || modelCalls.Load() != 2 {
		t.Fatal("unmatched tool escaped replay", w.Code, toolCalls.Load(), modelCalls.Load())
	}
	if w = post("http://model.test/api/chat", known); w.Code == 200 || modelCalls.Load() != 2 {
		t.Fatal("live request budget bypassed")
	}
}
