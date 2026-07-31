package replay

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"infernosim/pkg/event"
)

func writeReplayLog(t *testing.T, evt event.Event) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "events.log")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.NewEncoder(f).Encode(evt); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLegacyReplayRewritesToSelectedTarget(t *testing.T) {
	originalCalls := 0
	original := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		originalCalls++
		w.WriteHeader(http.StatusTeapot)
	}))
	defer original.Close()

	targetCalls := 0
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetCalls++
		if r.URL.Path != "/captured/path" {
			t.Errorf("path = %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()

	logPath := writeReplayLog(t, event.Event{
		Type:      "OutboundCall",
		Method:    http.MethodGet,
		URL:       original.URL + "/captured/path",
		Status:    http.StatusOK,
		Timestamp: time.Now(),
	})
	replayer, err := NewReplayer(logPath, ReplayConfig{Strict: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := replayer.PreflightValidation(target.URL); err != nil {
		t.Fatal(err)
	}
	if err := replayer.Replay(); err != nil {
		t.Fatal(err)
	}
	if originalCalls != 0 || targetCalls != 1 {
		t.Fatalf("originalCalls=%d targetCalls=%d", originalCalls, targetCalls)
	}
}

func TestLegacyReplayBlocksWritesByDefault(t *testing.T) {
	targetCalls := 0
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetCalls++
	}))
	defer target.Close()
	logPath := writeReplayLog(t, event.Event{
		Type:      "OutboundCall",
		Method:    http.MethodPost,
		URL:       "http://production.invalid/resource",
		Timestamp: time.Now(),
	})
	replayer, err := NewReplayer(logPath, ReplayConfig{Strict: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := replayer.PreflightValidation(target.URL); err != nil {
		t.Fatal(err)
	}
	if err := replayer.Replay(); err == nil {
		t.Fatal("write replay unexpectedly succeeded")
	}
	if targetCalls != 0 {
		t.Fatalf("target received %d write requests", targetCalls)
	}
}
