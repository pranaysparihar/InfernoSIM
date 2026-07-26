package stubproxy

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"infernosim/pkg/event"
)

func writeOutboundFixture(t *testing.T, events ...event.Event) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "outbound.log")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	enc := json.NewEncoder(f)
	for _, evt := range events {
		if err := enc.Encode(evt); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestStubReplaysCapturedResponse(t *testing.T) {
	path := writeOutboundFixture(t, event.Event{
		Type:               "OutboundCall",
		Method:             http.MethodGet,
		URL:                "http://dependency.test/api/value",
		Status:             http.StatusAccepted,
		ResponseCaptured:   true,
		ResponseHeaders:    http.Header{"Content-Type": {"application/json"}, "X-Contract": {"v1"}},
		ResponseBodyB64:    base64.StdEncoding.EncodeToString([]byte(`{"value":42}`)),
		ResponseBodySha256: "unused-by-stub",
	})
	stub, err := New(path, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	stub.ConfigureReplayCardinality(false, 1)

	req := httptest.NewRequest(http.MethodGet, "http://dependency.test/api/value", nil)
	rec := httptest.NewRecorder()
	stub.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d", rec.Code)
	}
	if rec.Header().Get("X-Contract") != "v1" || rec.Body.String() != `{"value":42}` {
		t.Fatalf("response headers/body not replayed: headers=%v body=%q", rec.Header(), rec.Body.String())
	}
}

func TestStubFanoutMatchesUnorderedCalls(t *testing.T) {
	path := writeOutboundFixture(t,
		event.Event{Type: "OutboundCall", Method: http.MethodGet, URL: "http://dependency.test/a", Status: 200},
		event.Event{Type: "OutboundCall", Method: http.MethodGet, URL: "http://dependency.test/b", Status: 204},
	)
	stub, err := New(path, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	stub.ConfigureReplayCardinality(true, 20)

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		for _, path := range []string{"/a", "/b"} {
			wg.Add(1)
			go func(path string) {
				defer wg.Done()
				req := httptest.NewRequest(http.MethodGet, "http://dependency.test"+path, nil)
				rec := httptest.NewRecorder()
				stub.ServeHTTP(rec, req)
				if rec.Code >= 400 {
					t.Errorf("%s returned %d", path, rec.Code)
				}
			}(path)
		}
	}
	wg.Wait()

	if got := stub.DivergenceReasons(); len(got) != 0 {
		t.Fatalf("unexpected divergences: %v", got)
	}
	if stub.ObservedCount() != 20 {
		t.Fatalf("observed = %d", stub.ObservedCount())
	}
}

func TestStubAllowsMissingOutboundLog(t *testing.T) {
	stub, err := New(filepath.Join(t.TempDir(), "missing.log"), "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if stub.ExpectedCount() != 0 {
		t.Fatalf("expected count = %d", stub.ExpectedCount())
	}
}
