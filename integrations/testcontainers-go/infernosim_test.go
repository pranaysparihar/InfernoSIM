package infernosim

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestIncidentFilesRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "inbound.log"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := incidentFiles(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "inbound.log"), filepath.Join(dir, "escape.log")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := incidentFiles(dir); err == nil {
		t.Fatal("expected symlink rejection")
	}
}

func TestContainerIntegration(t *testing.T) {
	image := os.Getenv("INFERNOSIM_TEST_IMAGE")
	if image == "" {
		t.Skip("set INFERNOSIM_TEST_IMAGE to run the Docker integration")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "inbound.log"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	event := map[string]any{
		"id": "dependency", "type": "OutboundCall", "timestamp": "2026-01-01T00:00:00Z",
		"method": "GET", "url": "http://dependency.test/value", "status": 200,
		"responseCaptured": true,
		"responseHeaders":  map[string][]string{"Content-Type": {"application/json"}},
		"responseBodyB64":  base64.StdEncoding.EncodeToString([]byte(`{"source":"infernosim"}`)),
	}
	line, _ := json.Marshal(event)
	if err := os.WriteFile(filepath.Join(dir, "outbound.log"), append(line, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	container, err := Run(ctx, Options{Image: image, IncidentDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		terminateContext, terminateCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer terminateCancel()
		_ = container.Terminate(terminateContext)
	})
	proxy, err := url.Parse(container.ProxyURL)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxy)}, Timeout: 10 * time.Second}
	response, err := client.Get("http://dependency.test/value")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("stub status=%s", response.Status)
	}
	if err := container.Reset(ctx); err != nil {
		t.Fatal(err)
	}
}
