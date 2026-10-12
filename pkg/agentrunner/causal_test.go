package agentrunner

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"infernosim/pkg/agentotel"
	"infernosim/pkg/agentreliability"
	"infernosim/pkg/causal"
	"infernosim/pkg/event"
	"infernosim/pkg/mcpproxy"
)

func TestCausalEvidenceIncludesUnmappedOTelAncestors(t *testing.T) {
	dir := causalIncident(t, "http")
	spans := []agentotel.Span{{Version: 1, TraceID: "trace", SpanID: "root"}, {Version: 1, TraceID: "trace", SpanID: "middle", ParentSpanID: "root"}, {Version: 1, TraceID: "trace", SpanID: "child", ParentSpanID: "middle"}}
	write := func() {
		var b bytes.Buffer
		for _, s := range spans {
			if err := json.NewEncoder(&b).Encode(s); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(filepath.Join(dir, "agent-spans.jsonl"), b.Bytes(), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write()
	g, err := DiscoverCausal(dir, causal.Declarations{Review: "fixture", SpanOperations: map[string]string{"trace/root": "one", "trace/child": "two"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateCausalRecording(dir, g); err != nil {
		t.Fatal(err)
	}
	spans[1].ParentSpanID = ""
	write()
	if err := ValidateCausalRecording(dir, g); err == nil {
		t.Fatal("unmapped ancestor changed without invalidating proof")
	}
}

func TestLargeCausalManifestCanBeLoadedAfterExport(t *testing.T) {
	incident := causalIncident(t, "http")
	g, err := DiscoverCausal(incident, causal.Declarations{Review: strings.Repeat("reviewed ", 140000)})
	if err != nil {
		t.Fatal(err)
	}
	p, err := causal.Generate(g, 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	s := p.Schedules[0]
	proof := causal.Execution{GraphHash: s.GraphHash, ScheduleID: s.ID, Observed: s.Order, Completed: true}
	for _, o := range g.Operations {
		proof.Bindings = append(proof.Bindings, causal.Binding{Operation: o.ID})
	}
	trial := Trial{Case: agentreliability.Case{ID: "large", ScheduleID: s.ID}, Causal: &causal.Replay{Graph: g, Schedule: s, Timeout: "1s"}}
	dest := filepath.Join(t.TempDir(), "large")
	if err := ExportReproduction(dest, incident, "", Reproduction{Trial: trial, CausalExecution: &proof, Timeout: time.Second, CheckTimeout: time.Second}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dest, "reproduction.json"))
	if err != nil || info.Size() <= 1<<20 {
		t.Fatalf("fixture did not cross old manifest limit: %v", err)
	}
	if _, err := LoadReproduction(dest); err != nil {
		t.Fatal(err)
	}
}

func TestPublishedCausalReproductionsRemainPortable(t *testing.T) {
	dirs, err := filepath.Glob(filepath.Join("..", "..", "benchmarks", "fixtures", "causal-v4.2", "*-reproduction"))
	if err != nil || len(dirs) != 15 {
		t.Fatalf("published reproduction inventory: %d %v", len(dirs), err)
	}
	for _, dir := range dirs {
		if _, err := LoadReproduction(dir); err != nil {
			t.Fatalf("%s: %v", dir, err)
		}
	}
}

func causalIncident(t *testing.T, transport string) string {
	t.Helper()
	dir := t.TempDir()
	for name, data := range map[string]string{"incident.json": `{}`, "inbound.log": "", "outbound.log": "", "replay.yaml": "agent:\n  enabled: true\n  adapters:\n    mcp: true\n"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	var events bytes.Buffer
	var transcript bytes.Buffer
	for i, name := range []string{"one", "two"} {
		request := []byte(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"tools/call","params":{"name":%q}}`, i+1, name))
		response := []byte(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":{"name":%q}}`, i+1, name))
		if transport == "http" || (transport == "mixed" && i == 0) {
			e := event.Event{ID: name, Type: "OutboundCall", Method: "POST", URL: "http://mcp.test/mcp", Status: 200, BodyB64: base64.StdEncoding.EncodeToString(request), ResponseBodyB64: base64.StdEncoding.EncodeToString(response), ResponseCaptured: true}
			_ = json.NewEncoder(&events).Encode(e)
		} else {
			for j, m := range [][]byte{request, response} {
				direction := "client_to_server"
				if j == 1 {
					direction = "server_to_client"
				}
				_ = json.NewEncoder(&transcript).Encode(mcpproxy.Record{Version: 1, Sequence: int64(i*2 + j + 1), Direction: direction, Message: m})
			}
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "outbound.log"), events.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	if transcript.Len() > 0 {
		if err := os.WriteFile(filepath.Join(dir, "mcp.log"), transcript.Bytes(), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func causalHelper(mode string) []string {
	return []string{os.Args[0], "-test.run=TestCausalApplicationHelper", "--", mode}
}

func TestCausalRunnerHTTPStdioAndMixedShareOneScheduler(t *testing.T) {
	for _, transport := range []string{"http", "stdio", "mixed"} {
		t.Run(transport, func(t *testing.T) {
			incident := causalIncident(t, transport)
			g, err := DiscoverCausal(incident, causal.Declarations{Review: "two independent application requests"})
			if err != nil {
				t.Fatal(err)
			}
			ids := []string{"one", "two"}
			if transport == "stdio" {
				ids = []string{"mcp_1", "mcp_3"}
			} else if transport == "mixed" {
				ids[1] = "mcp_3"
			}
			order := []string{causal.Admit(ids[0]), causal.Admit(ids[1]), causal.Release(ids[1]), causal.Release(ids[0])}
			s := causal.NewSchedule(g, order)
			trial := Trial{Case: agentreliability.Case{ID: "integration", ScheduleID: s.ID}, Causal: &causal.Replay{Graph: g, Schedule: s, Timeout: "2s"}}
			p, err := Prepare(incident, "", 0, 1, true)
			if err != nil {
				t.Fatal(err)
			}
			opts, err := RunTrial(Options{IncidentDir: incident, Command: causalHelper(transport), MCPConnector: causalHelper("connect"), Timeout: 5 * time.Second, Environment: []string{"GORACE=atexit_sleep_ms=0"}}, p.Config.Agent, trial)
			if err != nil {
				t.Fatal(err)
			}
			r, err := Run(context.Background(), opts)
			if err != nil {
				t.Fatal(err)
			}
			if !r.Passed || r.InvalidRun {
				t.Fatalf("%s failed: %+v", transport, r)
			}
			if r.CausalExecution == nil || !reflect.DeepEqual(r.CausalExecution.Observed, order) {
				t.Fatal("missing exact order proof")
			}
			if err := r.CausalExecution.Validate(g, s); err != nil {
				t.Fatal(err)
			}
			artifact := filepath.Join(t.TempDir(), "regression")
			manifest := Reproduction{Trial: trial, CausalExecution: r.CausalExecution, Timeout: 5 * time.Second, CheckTimeout: time.Second}
			if err := ExportReproduction(artifact, incident, p.ConfigPath, manifest); err != nil {
				t.Fatal(err)
			}
			moved := filepath.Join(t.TempDir(), "moved")
			if err := os.Rename(artifact, moved); err != nil {
				t.Fatal(err)
			}
			loaded, err := LoadReproduction(moved)
			if err != nil {
				t.Fatal(err)
			}
			opts.IncidentDir = filepath.Join(moved, "incident")
			opts.ConfigPath = ""
			opts, err = RunTrial(opts, p.Config.Agent, loaded.Trial)
			if err != nil {
				t.Fatal(err)
			}
			r, err = Run(context.Background(), opts)
			if err != nil || !r.Passed {
				t.Fatalf("relocated replay failed: %+v %v", r, err)
			}
			loaded.Trial.Causal.Schedule.Order[0], loaded.Trial.Causal.Schedule.Order[1] = loaded.Trial.Causal.Schedule.Order[1], loaded.Trial.Causal.Schedule.Order[0]
			if err := WriteJSON(filepath.Join(moved, "reproduction.json"), loaded); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadReproduction(moved); err == nil {
				t.Fatal("accepted altered schedule manifest")
			}
		})
	}
}

func TestCausalPlanAndMinimizationPreserveGraph(t *testing.T) {
	incident := causalIncident(t, "http")
	g, err := DiscoverCausal(incident, causal.Declarations{Review: "independent fixture"})
	if err != nil {
		t.Fatal(err)
	}
	cfg := agentreliability.Config{Enabled: true}
	p, err := PlanExploration(cfg, "scope", ExploreOptions{CausalGraph: &g, Seed: 7, Budget: 100, ScheduleBudget: 100, MaxFaults: 1})
	if err != nil {
		t.Fatal(err)
	}
	if p.Truncated || len(p.Trials) != 6 {
		t.Fatalf("expected six boundary interleavings: %+v", p)
	}
	again, _ := PlanExploration(cfg, "scope", p.Options)
	if !reflect.DeepEqual(p, again) {
		t.Fatal("unstable seeded cases")
	}
	var trial Trial
	for _, x := range p.Trials {
		if x.Causal.Schedule.Order[0] == "two:admit" {
			trial = x
			break
		}
	}
	r, err := MinimizeTrial(context.Background(), trial, "state:failure", 100, func(ctx context.Context, candidate Trial) (Result, error) {
		if err := candidate.Causal.Schedule.Validate(candidate.Causal.Graph); err != nil {
			return Result{}, err
		}
		if causal.Hash(candidate.Causal.Graph) != causal.Hash(g) {
			t.Fatalf("minimizer changed evidence graph")
		}
		proof := &causal.Execution{ScheduleID: candidate.Causal.Schedule.ID, GraphHash: candidate.Causal.Schedule.GraphHash, Observed: candidate.Causal.Schedule.Order, Completed: true}
		for _, o := range g.Operations {
			proof.Bindings = append(proof.Bindings, causal.Binding{Operation: o.ID})
		}
		return Result{CausalExecution: proof, Assertions: []agentreliability.AssertionResult{{ID: "state:failure", Coverage: "violated", Passed: false}}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !r.Complete || r.CausalExecution == nil {
		t.Fatal("minimization incomplete")
	}
	if err := r.CausalExecution.Validate(g, r.Trial.Causal.Schedule); err != nil {
		t.Fatal(err)
	}
}

func TestCausalRestartPreservesCommittedAdmission(t *testing.T) {
	incident := causalIncident(t, "http")
	g, err := DiscoverCausal(incident, causal.Declarations{Review: "application persists progress across explicit restart"})
	if err != nil {
		t.Fatal(err)
	}
	s := causal.NewSchedule(g, []string{"one:admit", "one:release", "two:admit", "two:release"})
	trial := Trial{Case: agentreliability.Case{ID: "restart", ScheduleID: s.ID}, Causal: &causal.Replay{Graph: g, Schedule: s, Timeout: "3s"}}
	p, err := Prepare(incident, "", 0, 1, true)
	if err != nil {
		t.Fatal(err)
	}
	opts, err := RunTrial(Options{IncidentDir: incident, Command: causalHelper("restart-http"), RestartAfterCall: 1, Timeout: 5 * time.Second, Environment: []string{"GORACE=atexit_sleep_ms=0"}}, p.Config.Agent, trial)
	if err != nil {
		t.Fatal(err)
	}
	r, err := Run(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Passed || r.InvalidRun || !r.Process.Restarted {
		t.Fatalf("restart failed: %+v", r)
	}
}

func TestCausalApplicationHelper(t *testing.T) {
	index := -1
	for i, arg := range os.Args {
		if arg == "--" {
			index = i
		}
	}
	if index < 0 || index+1 >= len(os.Args) {
		return
	}
	mode := os.Args[index+1]
	if mode == "connect" {
		if err := mcpproxy.Connect(context.Background(), os.Getenv("INFERNOSIM_MCP_ENDPOINT"), os.Getenv("INFERNOSIM_MCP_TOKEN"), os.Stdin, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(7)
		}
		os.Exit(0)
	}
	var wg sync.WaitGroup
	errors := make(chan error, 2)
	httpCall := func(name string) {
		defer wg.Done()
		proxy, _ := url.Parse(os.Getenv("INFERNOSIM_PROXY_URL"))
		transport := &http.Transport{Proxy: http.ProxyURL(proxy)}
		defer transport.CloseIdleConnections()
		client := &http.Client{Transport: transport, Timeout: 3 * time.Second}
		id := "runtime-" + name
		body := fmt.Sprintf(`{"jsonrpc":"2.0","id":%q,"method":"tools/call","params":{"name":%q}}`, id, name)
		response, err := client.Post("http://mcp.test/mcp", "application/json", strings.NewReader(body))
		if err != nil {
			errors <- err
			return
		}
		defer response.Body.Close()
		data, err := io.ReadAll(response.Body)
		if err != nil {
			errors <- err
			return
		}
		v, err := causal.Decode(data)
		if err != nil {
			errors <- err
			return
		}
		m := v.(map[string]any)
		if m["id"] != id || m["result"].(map[string]any)["name"] != name {
			errors <- fmt.Errorf("wrong HTTP correlation")
		}
	}
	if mode == "restart-http" {
		name := "one"
		if os.Getenv("INFERNOSIM_ATTEMPT") != "0" {
			name = "two"
		}
		wg.Add(1)
		go httpCall(name)
	}
	if mode == "http" || mode == "mixed" {
		wg.Add(1)
		go httpCall("one")
	}
	if mode == "http" {
		wg.Add(1)
		go httpCall("two")
	}
	if mode == "stdio" || mode == "mixed" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var argv []string
			if err := json.Unmarshal([]byte(os.Getenv("INFERNOSIM_MCP_COMMAND_JSON")), &argv); err != nil {
				errors <- err
				return
			}
			command := exec.Command(argv[0], argv[1:]...)
			in, err := command.StdinPipe()
			if err != nil {
				errors <- err
				return
			}
			out, err := command.StdoutPipe()
			if err != nil {
				errors <- err
				return
			}
			command.Stderr = os.Stderr
			if err = command.Start(); err != nil {
				errors <- err
				return
			}
			names := []string{"two"}
			if mode == "stdio" {
				names = append(names, "one")
			}
			for _, name := range names {
				_, err = fmt.Fprintf(in, "{\"jsonrpc\":\"2.0\",\"id\":%q,\"method\":\"tools/call\",\"params\":{\"name\":%q}}\n", "runtime-"+name, name)
				if err != nil {
					errors <- err
					_ = in.Close()
					_ = command.Wait()
					return
				}
			}
			reader := bufio.NewReader(out)
			for range names {
				line, e := reader.ReadBytes('\n')
				if e != nil {
					errors <- e
					break
				}
				v, e := causal.Decode(line)
				if e != nil {
					errors <- e
					break
				}
				m := v.(map[string]any)
				name := m["result"].(map[string]any)["name"].(string)
				if m["id"] != "runtime-"+name {
					errors <- fmt.Errorf("wrong stdio correlation")
					break
				}
			}
			_ = in.Close()
			_, _ = io.Copy(io.Discard, out)
			if err = command.Wait(); err != nil {
				errors <- err
			}
		}()
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(8)
	}
	os.Exit(0)
}
