package agentrunner

import (
	"context"
	"encoding/json"
	"fmt"
	"infernosim/pkg/stubproxy"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"infernosim/pkg/agentreliability"
)

func TestExplorationDiscoversTriplesPositionsAndOrders(t *testing.T) {
	p, err := Prepare(writeAgentIncident(t), "", 0, 10, true)
	if err != nil {
		t.Fatal(err)
	}
	c := p.Config.Agent
	f := c.Faults[0]
	c.Faults = nil
	for _, id := range []string{"a", "b", "c"} {
		f.ID = id
		c.Faults = append(c.Faults, f)
	}
	c.Schedules = []agentreliability.Schedule{{ID: "parallel", Timeout: "1s", Steps: []agentreliability.Selector{{Kind: "mcp_tool_result", Tool: "a", CallID: "a"}, {Kind: "mcp_tool_result", Tool: "b", CallID: "b"}}}}
	opts := ExploreOptions{Seed: 7, Budget: 1000, MaxFaults: 3, Occurrences: 2, PermuteSchedule: "parallel"}
	before := agentreliability.StableHash(c)
	plan, err := PlanExploration(c, p.ScopeHash, opts)
	if err != nil {
		t.Fatal(err)
	}
	again, err := PlanExploration(c, p.ScopeHash, opts)
	if err != nil || !reflect.DeepEqual(plan, again) {
		t.Fatal("unstable plan", err)
	}
	if plan.Truncated || before != agentreliability.StableHash(c) {
		t.Fatal("mutated source or truncated full plan")
	}
	triple, position, reverse := false, false, false
	seen := map[string]bool{}
	for _, trial := range plan.Trials {
		if seen[trial.Case.ID] {
			t.Fatal("duplicate case")
		}
		seen[trial.Case.ID] = true
		if _, err := trial.AgentConfig(c); err != nil {
			t.Fatal(err)
		}
		triple = triple || len(trial.Case.ActiveFaults()) == 3
		position = position || trial.Occurrences["a"] == 2
		reverse = reverse || (trial.Schedule != nil && trial.Schedule.Steps[0].CallID == "b")
	}
	if !triple || !position || !reverse {
		t.Fatalf("triple=%t position=%t reverse=%t", triple, position, reverse)
	}
	opts.Budget = 2
	limited, err := PlanExploration(c, p.ScopeHash, opts)
	if err != nil || len(limited.Trials) != 2 || !limited.Truncated {
		t.Fatal(limited, err)
	}
	opts.PermuteSchedule = "missing"
	if _, err := PlanExploration(c, p.ScopeHash, opts); err == nil {
		t.Fatal("unknown schedule accepted")
	}
}

func TestMinimizationRemovesNoiseButPreservesNecessaryOrder(t *testing.T) {
	trial := Trial{Case: agentreliability.Case{ID: "test", FaultIDs: []string{"a", "b"}, ScheduleID: "order"}, Occurrences: map[string]int{"a": 4}, Schedule: &agentreliability.Schedule{ID: "order", Timeout: "1s", Steps: []agentreliability.Selector{{CallID: "a"}, {CallID: "noise"}, {CallID: "b"}}}}
	run := func(ctx context.Context, t Trial) (Result, error) {
		order := false
		if t.Schedule != nil {
			sequence := ""
			for _, s := range t.Schedule.Steps {
				sequence += s.CallID
			}
			order = strings.Contains(sequence, "a") && strings.HasSuffix(sequence, "b")
		}
		failed := contains(t.Case.ActiveFaults(), "a") && order
		return Result{Assertions: []agentreliability.AssertionResult{{ID: "state:balance", Passed: !failed, Coverage: map[bool]string{true: "violated", false: "exercised"}[failed]}}}, nil
	}
	r, err := MinimizeTrial(context.Background(), trial, "state:balance", 100, run)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Complete || len(r.Trial.Case.ActiveFaults()) != 1 || len(r.Trial.Schedule.Steps) != 2 || !r.Trial.Schedule.Partial || r.Trial.Occurrences["a"] != 1 {
		t.Fatalf("%+v", r)
	}
	limited, err := MinimizeTrial(context.Background(), trial, "state:balance", 2, run)
	if err != nil || limited.Complete {
		t.Fatal(limited, err)
	}
	unstable := 0
	_, err = MinimizeTrial(context.Background(), trial, "state:balance", 10, func(context.Context, Trial) (Result, error) {
		unstable++
		return Result{Assertions: []agentreliability.AssertionResult{{ID: "state:balance", Passed: unstable%2 == 0, Coverage: "violated"}}}, nil
	})
	if err == nil {
		t.Fatal("accepted unstable failure")
	}
	_, err = MinimizeTrial(context.Background(), trial, "state:balance", 10, func(context.Context, Trial) (Result, error) {
		return Result{InvalidRun: true, Assertions: []agentreliability.AssertionResult{{ID: "state:balance", Passed: false, Coverage: "violated"}}}, nil
	})
	if err == nil {
		t.Fatal("accepted invalid replay")
	}
}
func TestReproductionRoundTripRelocationAndTamperRejection(t *testing.T) {
	incident := writeAgentIncident(t)
	p, err := Prepare(incident, "", 0, 10, true)
	if err != nil {
		t.Fatal(err)
	}
	c, err := ResolveCase(p.Cases, "refund-response-lost")
	if err != nil {
		t.Fatal(err)
	}
	original := filepath.Join(t.TempDir(), "repro")
	manifest := Reproduction{Trial: Trial{Case: c}, Timeout: time.Second, CheckTimeout: time.Second}
	if err := ExportReproduction(original, incident, p.ConfigPath, manifest); err != nil {
		t.Fatal(err)
	}
	moved := filepath.Join(t.TempDir(), "moved")
	if err := os.Rename(original, moved); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadReproduction(moved); err != nil {
		t.Fatal(err)
	}
	if err := ExportReproduction(moved, incident, p.ConfigPath, manifest); err == nil {
		t.Fatal("overwrote artifact")
	}
	if err := os.WriteFile(filepath.Join(moved, "incident", "outbound.log"), []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadReproduction(moved); err == nil {
		t.Fatal("accepted corrupted evidence")
	}
	if err := ExportReproduction(filepath.Join(incident, "recursive"), incident, p.ConfigPath, manifest); err == nil {
		t.Fatal("recursive export")
	}
	if err := os.Symlink(filepath.Join(incident, "replay.yaml"), filepath.Join(incident, "link")); err == nil {
		if err := ExportReproduction(filepath.Join(t.TempDir(), "links"), incident, p.ConfigPath, manifest); err == nil {
			t.Fatal("exported symlink")
		}
	}
}

func stateHelper(mode string) []string {
	return []string{os.Args[0], "-test.run=TestStateHookHelper", "--", mode}
}
func TestStateHooksCheckActualFilesAndRejectInvalidOutput(t *testing.T) {
	incident := writeAgentIncident(t)
	prepared, err := Prepare(incident, "", 0, 1, true)
	if err != nil {
		t.Fatal(err)
	}
	prepared.Config.Agent.Assertions = nil
	for _, mode := range []string{"check-safe", "check-failure", "missing", "duplicate", "unknown", "malformed", "null", "exit", "oversized"} {
		t.Run(mode, func(t *testing.T) {
			r, err := Run(context.Background(), Options{IncidentDir: incident, AgentConfig: &prepared.Config.Agent, Command: stateHelper("app"), Environment: []string{"GORACE=atexit_sleep_ms=0"}, StateCheck: StateCheckOptions{SetupCommand: stateHelper("setup"), Command: stateHelper(mode), IDs: []string{"balance"}, Timeout: time.Second}})
			if err != nil {
				t.Fatal(err)
			}
			if r.Passed != (mode == "check-safe") {
				t.Fatalf("%s: %+v", mode, r)
			}
			if r.InvalidRun != (mode != "check-safe" && mode != "check-failure") {
				t.Fatalf("incorrect validity %s %+v", mode, r)
			}
			if strings.Contains(r.Failure, "PRIVATE_SECRET") {
				t.Fatal("leaked probe output")
			}
			if r.StateCheckHash == "" {
				t.Fatal("missing check identity")
			}
		})
	}
}
func TestStateHookTimeoutAndScope(t *testing.T) {
	_, err := runStateCommand(context.Background(), stateHelper("sleep"), os.Environ(), 20*time.Millisecond)
	if err == nil {
		t.Fatal("hook ignored timeout")
	}
	if err := (StateCheckOptions{Command: stateHelper("missing")}).Validate(); err == nil {
		t.Fatal("missing expected check IDs accepted")
	}
	b := Result{Passed: true, ScopeHash: "same", Case: agentreliability.Case{ID: "one"}, StateCheckHash: "original"}
	c := b
	c.StateCheckHash = "changed"
	if _, err := CompareResults([]Result{b}, []Result{c}); err == nil {
		t.Fatal("compared different state oracles")
	}
}
func TestStateHookHelper(t *testing.T) {
	index := -1
	for i, arg := range os.Args {
		if arg == "--" {
			index = i
		}
	}
	if index < 0 || index+1 == len(os.Args) {
		return
	}
	mode := os.Args[index+1]
	path := filepath.Join(os.Getenv("INFERNOSIM_STATE_DIR"), "balance")
	switch mode {
	case "parallel":
		proxy, e := url.Parse(os.Getenv("INFERNOSIM_PROXY_URL"))
		if e != nil {
			os.Exit(9)
		}
		client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxy)}}
		var wg sync.WaitGroup
		for _, id := range []string{"a", "b"} {
			wg.Add(1)
			go func(id string) {
				defer wg.Done()
				body := `{"jsonrpc":"2.0","id":"` + id + `","method":"tools/call","params":{"name":"payment.refund","arguments":{"payment_id":"p1"}}}`
				response, e := client.Post("http://mcp.test/mcp", "application/json", strings.NewReader(body))
				if e != nil {
					os.Exit(10)
				}
				_ = response.Body.Close()
			}(id)
		}
		wg.Wait()
		client.CloseIdleConnections()
	case "setup":
		if err := os.WriteFile(path, []byte("0"), 0o600); err != nil {
			os.Exit(4)
		}
	case "app":
		b, err := os.ReadFile(path)
		if err != nil || string(b) != "0" {
			os.Exit(5)
		}
		if err := os.WriteFile(path, []byte("1"), 0o600); err != nil {
			os.Exit(6)
		}
	case "check-safe", "check-failure":
		b, err := os.ReadFile(path)
		passed := err == nil && string(b) == "1" && mode == "check-safe"
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"assertions": []any{map[string]any{"id": "balance", "passed": passed}}})
	case "missing":
		fmt.Print(`{"assertions":[]}`)
	case "duplicate":
		fmt.Print(`{"assertions":[{"id":"balance","passed":true},{"id":"balance","passed":true}]}`)
	case "unknown":
		fmt.Print(`{"assertions":[{"id":"other","passed":true}]}`)
	case "null":
		fmt.Print(`{"assertions":[{"id":"balance","passed":null}]}`)
	case "malformed":
		fmt.Print("PRIVATE_SECRET invalid JSON")
	case "oversized":
		fmt.Print(strings.Repeat("PRIVATE_SECRET", 10000))
	case "exit":
		fmt.Fprint(os.Stderr, "PRIVATE_SECRET")
		os.Exit(3)
	case "sleep":
		time.Sleep(time.Second)
	default:
		os.Exit(7)
	}
	os.Exit(0)
}

func TestTrialClonePreservesLargeIntegerAssertions(t *testing.T) {
	c := agentreliability.Config{Enabled: true, Assertions: []agentreliability.Assertion{{ID: "identity", Type: "request_matches", Tool: "lookup", Path: "$.arguments.id", Expected: int64(9007199254740993)}}}
	cloned, err := (Trial{}).AgentConfig(c)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(cloned.Assertions[0].Expected)
	if string(b) != "9007199254740993" {
		t.Fatalf("lost integer precision: %s", b)
	}
}

func TestExploreSchedulesDriveConcurrentApplication(t *testing.T) {
	incident := writeAgentIncident(t)
	p, err := Prepare(incident, "", 0, 1, true)
	if err != nil {
		t.Fatal(err)
	}
	p.Config.Agent.Assertions = nil
	p.Config.Agent.Schedules = []agentreliability.Schedule{{ID: "two", Timeout: "5s", Steps: []agentreliability.Selector{{CallID: "a"}, {CallID: "b"}}}}
	plan, err := PlanExploration(p.Config.Agent, p.ScopeHash, ExploreOptions{Budget: 20, MaxFaults: 1, PermuteSchedule: "two"})
	if err != nil {
		t.Fatal(err)
	}
	tested := 0
	for _, trial := range plan.Trials {
		if trial.Schedule == nil || len(trial.Case.ActiveFaults()) > 0 {
			continue
		}
		opts, err := RunTrial(Options{IncidentDir: incident, Command: stateHelper("parallel"), Environment: []string{"GORACE=atexit_sleep_ms=0"}}, p.Config.Agent, trial)
		if err != nil {
			t.Fatal(err)
		}
		r, err := Run(context.Background(), opts)
		if err != nil {
			t.Fatal(err)
		}
		if !r.Passed || r.InvalidRun {
			t.Fatalf("%+v", r)
		}
		calls := r.Simulator.(stubproxy.Snapshot).Agent.Calls
		if len(calls) != 2 || calls[0].CallID != trial.Schedule.Steps[0].CallID {
			t.Fatal("generated schedule did not control admission")
		}
		tested++
	}
	if tested != 2 {
		t.Fatalf("tested %d orderings", tested)
	}
}
