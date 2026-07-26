package replaydriver

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"infernosim/pkg/event"
)

func TestReplaySafeModeSkipsWrites(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()

	result, err := ReplayEvents([]event.Event{{
		Method:    http.MethodPost,
		URL:       "http://captured.test/resource",
		Timestamp: time.Now(),
	}}, server.URL, ReplayConfig{TimeScale: 1, Density: 1, SafeMode: true})
	if err != nil {
		t.Fatal(err)
	}
	if requests != 0 || result.SafeModeSkipped != 1 || result.CompletedEvents != 0 {
		t.Fatalf("requests=%d skipped=%d completed=%d", requests, result.SafeModeSkipped, result.CompletedEvents)
	}
}

func TestReplayFingerprintIncludesResponseBody(t *testing.T) {
	body := "first"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()
	events := []event.Event{{Method: http.MethodGet, URL: "http://captured.test/value", Timestamp: time.Now()}}
	cfg := ReplayConfig{TimeScale: 1, Density: 1}

	first, err := ReplayEvents(events, server.URL, cfg)
	if err != nil {
		t.Fatal(err)
	}
	body = "second"
	second, err := ReplayEvents(events, server.URL, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if first.Fingerprint == second.Fingerprint {
		t.Fatal("response body change did not alter determinism fingerprint")
	}
}

func TestCompareEventsUsesResponseHeadersNotRequestHeaders(t *testing.T) {
	captured := event.Event{
		Status:           200,
		Headers:          http.Header{"Authorization": {"Bearer captured"}},
		ResponseCaptured: true,
		ResponseHeaders:  http.Header{"Content-Type": {"application/json"}},
	}
	replayed := event.Event{
		Status:           200,
		ResponseCaptured: true,
		ResponseHeaders:  http.Header{"Content-Type": {"application/json"}},
	}
	if diff := CompareEvents(captured, replayed, 1); diff != nil {
		t.Fatalf("request headers produced a false response diff: %+v", diff)
	}
}

func TestLoadReplayConfigRejectsUnknownFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "replay.yaml")
	writeTestFile(t, path, []byte("target: http://localhost\nunknown: true\n"))
	if _, err := LoadReplayConfig(path); err == nil {
		t.Fatal("unknown replay config field was accepted")
	}
}

func writeTestFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}
