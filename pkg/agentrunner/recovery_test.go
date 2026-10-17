package agentrunner

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"infernosim/pkg/agentreliability"
	"infernosim/pkg/causal"
	"infernosim/pkg/event"
	"infernosim/pkg/liverecovery"
)

const initialRecoveryRequest = `{"model":"fixture","stream":false,"messages":[{"role":"user","content":"look up item"}]}`

func recoveryModelResponse(item string) string {
	return fmt.Sprintf(`{"model":"fixture","message":{"role":"assistant","content":"","tool_calls":[{"id":"proposal_%s","function":{"name":"lookup","arguments":{"item":%q}}}]},"done":true,"eval_count":4}`, item, item)
}
func recoveryToolRequest(item, id string) string {
	return fmt.Sprintf(`{"jsonrpc":"2.0","id":%q,"method":"tools/call","params":{"name":"lookup","arguments":{"item":%q}}}`, id, item)
}

func recoveryIncident(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range map[string]string{"incident.json": "{}", "inbound.log": "", "replay.yaml": `matching:
  rules:
    - name: exact-json
      methods: [POST]
      compare_json: true
agent:
  enabled: true
  adapters:
    mcp: true
    providers:
      - name: local-model
        type: ollama
        host_regex: '^model\.test$'
  limits:
    max_calls: 10
  faults:
    - id: tool-fails
      select:
        kind: mcp_tool_result
        tool: lookup
        occurrence: 1
      status: 503
  assertions:
    - id: recorded-tools-only
      type: no_unexpected_calls
`} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	var buf bytes.Buffer
	for i, pair := range []struct{ url, request, response string }{
		{"http://model.test/api/chat", initialRecoveryRequest, recoveryModelResponse("bad")},
		{"http://tools.test/mcp", recoveryToolRequest("bad", "first"), `{"jsonrpc":"2.0","id":"first","result":{"ok":true}}`},
		{"http://tools.test/mcp", recoveryToolRequest("good", "retry"), `{"jsonrpc":"2.0","id":"retry","result":{"ok":true}}`},
	} {
		json.NewEncoder(&buf).Encode(event.Event{ID: fmt.Sprintf("input%d", i), Type: "OutboundCall", Method: "POST", URL: pair.url, Headers: map[string][]string{"Content-Type": {"application/json"}}, BodySize: int64(len(pair.request)), BodyB64: base64.StdEncoding.EncodeToString([]byte(pair.request)), Status: 200, ResponseCaptured: true, ResponseBodyB64: base64.StdEncoding.EncodeToString([]byte(pair.response)), ResponseHeaders: map[string][]string{"Content-Type": {"application/json"}}})
	}
	if err := os.WriteFile(filepath.Join(dir, "outbound.log"), buf.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestLiveRecoveryCapturedBranchReplaysOffline(t *testing.T) {
	var calls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		body, _ := io.ReadAll(r.Body)
		if !bytes.Contains(body, []byte("tool failed: 503")) {
			t.Error("fault context not preserved")
		}
		fmt.Fprint(w, recoveryModelResponse("good"))
	}))
	defer provider.Close()
	incident := recoveryIncident(t)
	command := []string{os.Args[0], "-test.run=TestLiveRecoveryApplicationHelper", "--", "recover"}
	base := Options{IncidentDir: incident, Command: command, Environment: []string{"GORACE=atexit_sleep_ms=0"}, Timeout: 5 * time.Second, Case: agentreliability.Case{ID: "hybrid", FaultID: "tool-fails"}}
	offline, err := Run(context.Background(), base)
	if err != nil {
		t.Fatal(err)
	}
	if !offline.InvalidRun || calls.Load() != 0 {
		t.Fatalf("offline gate: %+v calls=%d", offline, calls.Load())
	}
	capture := filepath.Join(t.TempDir(), "branch")
	base.LiveRecovery = &liverecovery.Options{Endpoint: provider.URL, Origin: "http://model.test", Model: "fixture", MaxRequests: 1, MaxTokens: 16, Timeout: time.Second, OutputDir: capture, CaptureSensitiveData: true, Review: "fixture awaits each model/tool response; no hidden dependencies"}
	result, err := Run(context.Background(), base)
	if err != nil || !result.Passed || result.InvalidRun || result.Recovery == nil || !result.Recovery.Hybrid || calls.Load() != 1 {
		t.Fatalf("hybrid: err=%v result=%+v calls=%d", err, result, calls.Load())
	}
	observed, err := ReadObserved(filepath.Join(capture, "observed.json"), capture)
	if err != nil {
		b, _ := os.ReadFile(filepath.Join(capture, "observed.json"))
		var debug ObservedCapture
		json.Unmarshal(b, &debug)
		t.Fatalf("branch not validated: %s: %v issues=%+v graphIssues=%+v", result.RecoveryCaptureStatus, err, debug.Issues, debug.Graph.Issues)
	}
	if len(observed.Graph.Operations) != 4 {
		t.Fatal("original branch was spliced instead of captured", len(observed.Graph.Operations))
	}
	if _, err := CompareResults([]Result{result}, []Result{result}); err == nil {
		t.Fatal("live run treated as deterministic comparison")
	}
	provider.Close()
	p, err := Prepare(capture, "", 0, 1, true)
	if err != nil {
		t.Fatal(err)
	}
	trial := Trial{Case: agentreliability.Case{ID: "offline-recovery", ScheduleID: observed.Schedule.ID}, Causal: &causal.Replay{Graph: observed.Graph, Schedule: *observed.Schedule, Timeout: "2s"}}
	opts, err := RunTrial(Options{IncidentDir: capture, Command: command, Timeout: 5 * time.Second, Environment: base.Environment}, p.Config.Agent, trial)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := Run(context.Background(), opts)
	if err != nil || !replayed.Passed || replayed.InvalidRun || calls.Load() != 1 {
		t.Fatalf("offline branch: %v %+v", err, replayed)
	}
}

func TestLiveRecoveryApplicationHelper(t *testing.T) {
	if len(os.Args) < 2 || os.Args[len(os.Args)-1] != "recover" {
		return
	}
	proxy, _ := url.Parse(os.Getenv("INFERNOSIM_PROXY_URL"))
	transport := &http.Transport{Proxy: http.ProxyURL(proxy)}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 3 * time.Second}
	post := func(endpoint, body string) (int, map[string]any) {
		response, err := client.Post(endpoint, "application/json", strings.NewReader(body))
		if err != nil {
			os.Exit(4)
		}
		defer response.Body.Close()
		var v map[string]any
		if json.NewDecoder(response.Body).Decode(&v) != nil {
			os.Exit(5)
		}
		return response.StatusCode, v
	}
	_, first := post("http://model.test/api/chat", initialRecoveryRequest)
	item := first["message"].(map[string]any)["tool_calls"].([]any)[0].(map[string]any)["function"].(map[string]any)["arguments"].(map[string]any)["item"].(string)
	status, _ := post("http://tools.test/mcp", recoveryToolRequest(item, "first"))
	if status != 503 {
		os.Exit(6)
	}
	_, next := post("http://model.test/api/chat", `{"model":"fixture","stream":false,"messages":[{"role":"user","content":"look up item"},{"role":"tool","tool_call_id":"proposal_bad","tool_name":"lookup","content":"tool failed: 503"}]}`)
	item = next["message"].(map[string]any)["tool_calls"].([]any)[0].(map[string]any)["function"].(map[string]any)["arguments"].(map[string]any)["item"].(string)
	status, _ = post("http://tools.test/mcp", recoveryToolRequest(item, "retry"))
	if status != 200 || item != "good" {
		os.Exit(7)
	}
	fmt.Println("RECOVERY_OK")
	os.Exit(0)
}
