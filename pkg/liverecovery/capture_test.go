package liverecovery

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"infernosim/pkg/event"
	"infernosim/pkg/privacy"
)

func TestCaptureRedactsPrefixAndStreamAndNeverRetainsAuthorization(t *testing.T) {
	r := recoveryFor(t, "http://127.0.0.1:11434")
	policy := &privacy.Policy{Version: 1, CaptureBodies: true, JSONFields: []privacy.JSONRule{{Path: "$.secret", Action: privacy.ActionRedact}, {Path: "$.message.content", Action: privacy.ActionRedact}}}
	r.options.Privacy = policy
	request := httptest.NewRequest("POST", "http://model.test/api/chat", nil)
	request.Header.Set("Authorization", "Bearer raw-credential")
	request.Header.Set("X-API-Key", "raw-api-key")
	w, finish := r.Observe(httptest.NewRecorder(), request, []byte(`{"secret":"request-secret"}`))
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"model":"fixture","message":{"content":"response-secret"},"done":true,"eval_count":1}` + "\n"))
	finish()
	if err := r.Seal([]byte("agent:\n  enabled: true\n"), r.Snapshot()); err != nil {
		t.Fatal(err)
	}
	files, err := os.ReadDir(r.options.OutputDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		b, err := os.ReadFile(filepath.Join(r.options.OutputDir, file.Name()))
		if err != nil {
			t.Fatal(err)
		}
		for _, secret := range []string{"raw-credential", "raw-api-key", "request-secret", "response-secret"} {
			if strings.Contains(string(b), secret) {
				t.Fatalf("secret leaked in %s", file.Name())
			}
		}
		if file.Name() == "outbound.log" {
			var e event.Event
			if err = json.Unmarshal(b, &e); err != nil {
				t.Fatal(err)
			}
			for _, encoded := range []string{e.BodyB64, e.ResponseBodyB64} {
				decoded, err := base64.StdEncoding.DecodeString(encoded)
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(string(decoded), "request-secret") || strings.Contains(string(decoded), "response-secret") || !strings.Contains(string(decoded), "REDACTED") {
					t.Fatal("body privacy transform failed", string(decoded))
				}
			}
		}
	}
}
