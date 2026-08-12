package simserver

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestServerHealthResetStatusAndProof(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "inbound.log"), []byte(""), 0o600); err != nil {
		t.Fatal(err)
	}
	event := map[string]any{
		"id": "dep-1", "type": "OutboundCall", "timestamp": "2026-01-01T00:00:00Z",
		"method": "GET", "url": "http://dependency.test/value", "status": 200,
		"responseCaptured": true, "responseBodyB64": base64.StdEncoding.EncodeToString([]byte(`{"ok":true}`)),
		"responseHeaders": map[string][]string{"Content-Type": {"application/json"}},
	}
	line, _ := json.Marshal(event)
	if err := os.WriteFile(filepath.Join(dir, "outbound.log"), append(line, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	server, err := New(Options{IncidentDir: dir, Listen: "127.0.0.1:0", AdminListen: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Close(ctx)
	})

	admin := "http://" + server.AdminAddress()
	response, err := http.Get(admin + "/healthz")
	if err != nil || response.StatusCode != http.StatusOK {
		t.Fatalf("health: status=%v err=%v", response, err)
	}
	_ = response.Body.Close()

	request, _ := http.NewRequest(http.MethodGet, "http://dependency.test/value", nil)
	request.Host = "dependency.test"
	response, err = http.DefaultClient.Do(rewriteToProxy(request, server.StubAddress()))
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("stub status=%d", response.StatusCode)
	}

	proof := fetchProof(t, admin)
	if proof.IncidentHash == "" || proof.SemanticHash == "" || proof.Snapshot.Observed != 1 {
		t.Fatalf("proof=%+v", proof)
	}

	response, err = http.Post(admin+"/__infernosim/reset", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	response, _ = http.Get(admin + "/__infernosim/status")
	var status map[string]any
	_ = json.NewDecoder(response.Body).Decode(&status)
	_ = response.Body.Close()
	if status["observed"] != float64(0) {
		t.Fatalf("status after reset=%v", status)
	}
	request, _ = http.NewRequest(http.MethodGet, "http://dependency.test/value", nil)
	request.Host = "dependency.test"
	response, err = http.DefaultClient.Do(rewriteToProxy(request, server.StubAddress()))
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	secondProof := fetchProof(t, admin)
	if secondProof.SemanticHash != proof.SemanticHash {
		t.Fatalf("proof is not deterministic: first=%s second=%s", proof.SemanticHash, secondProof.SemanticHash)
	}
}

func fetchProof(t *testing.T, admin string) Proof {
	t.Helper()
	response, err := http.Get(admin + "/__infernosim/proof")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var proof Proof
	if err := json.NewDecoder(response.Body).Decode(&proof); err != nil {
		t.Fatal(err)
	}
	return proof
}

func rewriteToProxy(request *http.Request, address string) *http.Request {
	clone := request.Clone(request.Context())
	clone.URL.Scheme = "http"
	clone.URL.Host = address
	clone.Header.Set("Host", request.Host)
	clone.Host = request.Host
	return clone
}
