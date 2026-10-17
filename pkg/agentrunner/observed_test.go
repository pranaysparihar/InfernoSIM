package agentrunner

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"infernosim/pkg/agentreliability"
	"infernosim/pkg/causal"
	"infernosim/pkg/mcpproxy"
	"infernosim/pkg/stubproxy"
	"infernosim/pkg/traceorder"
)

func TestRecordedObservedHTTPMCPRoundTrip(t *testing.T) {
	for _, transport := range []string{"http", "stdio", "mixed"} {
		t.Run(transport, func(t *testing.T) {
			second := make(chan struct{})
			var once sync.Once
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var m map[string]any
				if json.NewDecoder(r.Body).Decode(&m) != nil {
					http.Error(w, "bad input", 400)
					return
				}
				name := m["params"].(map[string]any)["name"]
				if transport == "http" && name == "one" {
					select {
					case <-second:
					case <-r.Context().Done():
						return
					}
				}
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": m["id"], "result": map[string]any{"name": name}})
				if name == "two" {
					http.NewResponseController(w).Flush()
					once.Do(func() { close(second) })
				}
			}))
			defer upstream.Close()
			t.Setenv("INFERNOSIM_TEST_TARGET", upstream.URL+"/mcp")
			t.Setenv("GORACE", "atexit_sleep_ms=0")
			incident := filepath.Join(t.TempDir(), "capture")
			o := RecordOptions{Incident: incident, Command: causalHelper(transport), MCPConnector: causalHelper("connect"), Timeout: 10 * time.Second, CaptureSensitiveData: true, AllowPrivateDestinations: true}
			if transport != "http" {
				o.MCPCommand = causalHelper("record-server")
			}
			if result, err := Record(context.Background(), o); err != nil {
				t.Fatalf("record: %v %+v", err, result)
			}
			observed, err := ConvertObserved(incident, causal.Declarations{Review: "independent test calls"})
			if err != nil {
				t.Fatal(err)
			}
			if observed.Schedule == nil {
				t.Fatalf("partial capture: %+v", observed.Issues)
			}
			path := filepath.Join(t.TempDir(), "observed.json")
			if err = WriteJSON(path, observed); err != nil {
				t.Fatal(err)
			}
			if _, err = ReadObserved(path, incident); err != nil {
				t.Fatal(err)
			}
			repeat, err := ConvertObserved(incident, causal.Declarations{Review: "independent test calls"})
			if err != nil || !reflect.DeepEqual(repeat, observed) {
				t.Fatal("unstable conversion", err)
			}
			p, err := Prepare(incident, "", 0, 1, true)
			if err != nil {
				t.Fatal(err)
			}
			trial := Trial{Case: agentreliability.Case{ID: "observed", ScheduleID: observed.Schedule.ID}, Causal: &causal.Replay{Graph: observed.Graph, Schedule: *observed.Schedule, Timeout: "3s"}}
			opts, err := RunTrial(Options{IncidentDir: incident, Command: causalHelper(transport), MCPConnector: causalHelper("connect"), Timeout: 10 * time.Second}, p.Config.Agent, trial)
			if err != nil {
				t.Fatal(err)
			}
			result, err := Run(context.Background(), opts)
			if err != nil || !result.Passed || result.InvalidRun {
				t.Fatalf("replay: %v %+v", err, result)
			}
			if err = result.CausalExecution.Validate(observed.Graph, *observed.Schedule); err != nil {
				t.Fatal(err)
			}
			artifact := filepath.Join(t.TempDir(), "artifact")
			if err = ExportReproduction(artifact, incident, "", Reproduction{Trial: trial, CausalExecution: result.CausalExecution, Timeout: 10 * time.Second, CheckTimeout: time.Second}); err != nil {
				t.Fatal(err)
			}
			moved := filepath.Join(t.TempDir(), "moved")
			if err = os.Rename(artifact, moved); err != nil {
				t.Fatal(err)
			}
			manifest, err := LoadReproduction(moved)
			if err != nil {
				t.Fatal(err)
			}
			opts.IncidentDir = filepath.Join(moved, "incident")
			opts.ConfigPath = ""
			result, err = Run(context.Background(), opts)
			if err != nil || !result.Passed {
				t.Fatalf("relocated: %v %+v", err, result)
			}
			if err = result.CausalExecution.Validate(manifest.Trial.Causal.Graph, manifest.Trial.Causal.Schedule); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestObservedCaptureDoesNotGuessMissingOrIndependentClocks(t *testing.T) {
	for _, kind := range []string{"missing", "unrelated", "gap", "duplicate"} {
		t.Run(kind, func(t *testing.T) {
			incident := causalIncident(t, "http")
			events, err := stubproxy.LoadOutboundEvents(filepath.Join(incident, "outbound.log"))
			if err != nil {
				t.Fatal(err)
			}
			clock := traceorder.New()
			for i := range events {
				events[i].CaptureAdmit = clock.Next()
				events[i].CaptureRelease = clock.Next()
			}
			switch kind {
			case "missing":
				events[0].CaptureAdmit = nil
			case "unrelated":
				events[0].CaptureAdmit = traceorder.New().Next()
			case "gap":
				clock.Next()
			case "duplicate":
				events[0].CaptureRelease = events[0].CaptureAdmit
			}
			file, err := os.Create(filepath.Join(incident, "outbound.log"))
			if err != nil {
				t.Fatal(err)
			}
			for _, e := range events {
				json.NewEncoder(file).Encode(e)
			}
			file.Close()
			WriteJSON(filepath.Join(incident, "capture-session.json"), clock.Snapshot())
			r, err := ConvertObserved(incident, causal.Declarations{Review: "test"})
			if err != nil {
				t.Fatal(err)
			}
			if r.Schedule != nil || r.Status != "partial" || len(r.Issues) == 0 {
				t.Fatal("guessed exact order", r)
			}
		})
	}
}

func TestObservedMCPProgressAndCancellationTerminal(t *testing.T) {
	incident := causalIncident(t, "stdio")
	os.WriteFile(filepath.Join(incident, "outbound.log"), nil, 0600)
	clock := traceorder.New()
	messages := []struct{ direction, body string }{
		{"client_to_server", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"slow","_meta":{"progressToken":"p"}}}`},
		{"server_to_client", `{"jsonrpc":"2.0","method":"notifications/progress","params":{"progressToken":"p","progress":1}}`},
		{"client_to_server", `{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":1}}`},
	}
	f, err := os.Create(filepath.Join(incident, "mcp.log"))
	if err != nil {
		t.Fatal(err)
	}
	for i, m := range messages {
		json.NewEncoder(f).Encode(mcpproxy.Record{Version: 1, Sequence: int64(i + 1), Direction: m.direction, Message: json.RawMessage(m.body), Received: clock.Next(), Forwarded: clock.Next()})
	}
	f.Close()
	WriteJSON(filepath.Join(incident, "capture-session.json"), clock.Snapshot())
	r, err := ConvertObserved(incident, causal.Declarations{Review: "progress then cancellation"})
	if err != nil {
		t.Fatal(err)
	}
	if r.Schedule == nil {
		t.Fatal(r.Issues)
	}
	if !strings.HasSuffix(r.Schedule.Order[len(r.Schedule.Order)-1], "mcp_1:release") {
		t.Fatal("terminal boundary not retained", r.Schedule.Order)
	}
	if err = r.Schedule.Validate(r.Graph); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(incident, "capture-session.json")); err != nil {
		t.Fatal(err)
	}
	r, err = ConvertObserved(incident, causal.Declarations{Review: "same"})
	if err != nil || r.Schedule != nil || r.EvidenceComplete {
		t.Fatal("unsealed MCP capture became exact", err, r.Status)
	}
	if err := WriteJSON(filepath.Join(incident, "mcp.log.capture-session.json"), clock.Snapshot()); err != nil {
		t.Fatal(err)
	}
	r, err = ConvertObserved(incident, causal.Declarations{Review: "same"})
	if err != nil || r.Schedule == nil {
		t.Fatal("standalone MCP seal rejected", err, r.Issues)
	}
}
