package mcpsdk_test

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"infernosim/pkg/agentreliability"
	"infernosim/pkg/causal"
	"infernosim/pkg/mcpproxy"
)

func concurrentSDKSession(t *testing.T, ctx context.Context, path string, engine *agentreliability.Engine, denyTenantB bool, reverse bool) {
	t.Helper()
	requests, writeRequest := io.Pipe()
	responses, writeResponse := io.Pipe()
	defer requests.Close()
	defer writeRequest.Close()
	defer responses.Close()
	defer writeResponse.Close()
	done := make(chan error, 1)
	go func() {
		var err error
		if engine == nil {
			err = mcpproxy.RecordCommand(ctx, mcpproxy.RecordOptions{OutputPath: path, Command: []string{os.Args[0], "-test.run=TestSDKServerHelper", "--", "infernosim-sdk-helper"}, Stdin: requests, Stdout: writeResponse, Stderr: io.Discard})
		} else {
			err = mcpproxy.Replay(mcpproxy.ReplayOptions{Context: ctx, TranscriptPath: path, Stdin: requests, Stdout: writeResponse, Engine: engine})
		}
		_ = writeResponse.Close()
		done <- err
	}()
	client := mcp.NewClient(&mcp.Implementation{Name: "infernosim-concurrent-client", Version: "1.0.0"}, nil)
	session, err := client.Connect(ctx, &mcp.IOTransport{Reader: responses, Writer: writeRequest}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = session.ListTools(ctx, nil); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	failures := make(chan error, 2)
	tenants := []string{"tenant-a", "tenant-b"}
	if reverse {
		tenants[0], tenants[1] = tenants[1], tenants[0]
	}
	for _, tenant := range tenants {
		wg.Add(1)
		go func(tenant string) {
			defer wg.Done()
			result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "approve", Arguments: input{Tenant: tenant}})
			if err != nil {
				failures <- err
				return
			}
			content, ok := result.StructuredContent.(map[string]any)
			if !ok {
				failures <- fmt.Errorf("unexpected structured content %T", result.StructuredContent)
				return
			}
			want := tenant == "tenant-a" || !denyTenantB
			if content["approved"] != want {
				failures <- fmt.Errorf("wrong concurrent response/fault identity for %s: %+v", tenant, content)
			}
		}(tenant)
	}
	wg.Wait()
	close(failures)
	_ = session.Close()
	for err := range failures {
		t.Error(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}

func TestOfficialSDKConcurrentCausalSchedules(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	path := filepath.Join(t.TempDir(), "mcp.log")
	concurrentSDKSession(t, ctx, path, nil, true, false)
	records, err := mcpproxy.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	xs, err := mcpproxy.Exchanges(records)
	if err != nil {
		t.Fatal(err)
	}
	list := ""
	discovery := ""
	var tools []string
	targetID := ""
	for _, x := range xs {
		v, _ := causal.Decode(x.Request)
		m := v.(map[string]any)
		switch m["method"] {
		case "server/discover":
			discovery = x.ID
		case "tools/list":
			list = x.ID
		case "tools/call":
			tools = append(tools, x.ID)
			params := m["params"].(map[string]any)
			if params["arguments"].(map[string]any)["tenant"] == "tenant-b" {
				targetID = fmt.Sprint(m["id"])
			}
		}
	}
	if list == "" || len(tools) != 2 || targetID == "" {
		t.Fatalf("unexpected SDK recording: %+v", xs)
	}
	d := causal.Declarations{Review: "client awaits discovery, then invokes two independent approval requests concurrently"}
	if discovery != "" {
		d.Edges = append(d.Edges, causal.Declaration{From: causal.Release(discovery), To: causal.Admit(list), Reason: "this SDK application awaits Connect before calling ListTools; modern discovery itself is optional"})
	}
	for _, id := range tools {
		d.Edges = append(d.Edges, causal.Declaration{From: causal.Release(list), To: causal.Admit(id), Reason: "SDK application awaits ListTools before invoking tools"})
	}
	g, err := causal.Discover(xs, nil, d)
	if err != nil {
		t.Fatal(err)
	}
	for seed := int64(0); seed < 3; seed++ {
		plan, err := causal.Generate(g, seed, 100)
		if err != nil {
			t.Fatal(err)
		}
		if !plan.Exhaustive || len(plan.Schedules) != 6 {
			t.Fatalf("SDK graph did not expose six valid concurrent orders: schedules=%d exhaustive=%t", len(plan.Schedules), plan.Exhaustive)
		}
		for i, s := range plan.Schedules {
			for _, fault := range []bool{false, true} {
				cfg := agentreliability.Config{Enabled: true, Adapters: agentreliability.Adapters{MCP: true}, Causal: &causal.Replay{Graph: g, Schedule: s, Timeout: "2s"}, Faults: []agentreliability.Fault{{ID: "wrong-approval", Select: agentreliability.Selector{Tool: "approve", CallID: targetID}, Mutations: []agentreliability.Mutation{{Operation: "set", Path: "$.result.structuredContent.approved", Value: true}}}}}
				var active []string
				if fault {
					active = []string{"wrong-approval"}
				}
				engine, err := agentreliability.NewEngine(cfg, active)
				if err != nil {
					t.Fatal(err)
				}
				concurrentSDKSession(t, ctx, path, engine, !fault, i%2 == 0)
				if err := engine.Causal().Snapshot().Validate(g, s); err != nil {
					t.Fatal(err)
				}
				if got := len(engine.Snapshot().AppliedFaults); (fault && got != 1) || (!fault && got != 0) {
					t.Fatal("fault drifted across concurrent tool identities")
				}
			}
		}
	}
}
