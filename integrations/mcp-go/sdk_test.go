package mcpsdk_test

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"infernosim/pkg/agentreliability"
	"infernosim/pkg/mcpproxy"
)

type input struct {
	Tenant string `json:"tenant"`
}
type output struct {
	Approved bool `json:"approved"`
}

func TestSDKServerHelper(t *testing.T) {
	if os.Args[len(os.Args)-1] != "infernosim-sdk-helper" {
		return
	}
	server := mcp.NewServer(&mcp.Implementation{Name: "infernosim-fixture", Version: "1.0.0"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "approve", Description: "Synthetic approval fixture"}, func(ctx context.Context, req *mcp.CallToolRequest, in input) (*mcp.CallToolResult, output, error) {
		return nil, output{Approved: in.Tenant == "tenant-a"}, nil
	})
	if server.Run(context.Background(), &mcp.StdioTransport{}) != nil {
		os.Exit(2)
	}
	os.Exit(0)
}

func TestOfficialSDKRecordAndFaultReplay(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	transcript := filepath.Join(t.TempDir(), "mcp.log")
	for _, phase := range []string{"record", "replay", "reconnect"} {
		t.Run(phase, func(t *testing.T) {
			requests, writeRequest := io.Pipe()
			responses, writeResponse := io.Pipe()
			defer requests.Close()
			defer writeRequest.Close()
			defer responses.Close()
			defer writeResponse.Close()
			done := make(chan error, 1)
			var engine *agentreliability.Engine
			if phase == "record" {
				go func() {
					err := mcpproxy.RecordCommand(ctx, mcpproxy.RecordOptions{OutputPath: transcript, Command: []string{os.Args[0], "-test.run=TestSDKServerHelper", "--", "infernosim-sdk-helper"}, Stdin: requests, Stdout: writeResponse, Stderr: io.Discard})
					_ = writeResponse.Close()
					done <- err
				}()
			} else {
				c := agentreliability.Config{Adapters: agentreliability.Adapters{MCP: true}, Faults: []agentreliability.Fault{{ID: "deny", Select: agentreliability.Selector{Tool: "approve"}, Mutations: []agentreliability.Mutation{{Operation: "set", Path: "$.result.structuredContent.approved", Value: false}}}}}
				var err error
				engine, err = agentreliability.NewEngine(c, []string{"deny"})
				if err != nil {
					t.Fatal(err)
				}
				go func() {
					err := mcpproxy.Replay(mcpproxy.ReplayOptions{Context: ctx, TranscriptPath: transcript, Stdin: requests, Stdout: writeResponse, Engine: engine})
					_ = writeResponse.Close()
					done <- err
				}()
			}
			client := mcp.NewClient(&mcp.Implementation{Name: "infernosim-sdk-client", Version: "1.0.0"}, nil)
			session, err := client.Connect(ctx, &mcp.IOTransport{Reader: responses, Writer: writeRequest}, nil)
			if err != nil {
				t.Fatal(err)
			}
			tools, err := session.ListTools(ctx, nil)
			if err != nil || len(tools.Tools) != 1 {
				t.Fatalf("list tools: %+v %v", tools, err)
			}
			result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "approve", Arguments: input{Tenant: "tenant-a"}})
			if err != nil {
				t.Fatal(err)
			}
			content, ok := result.StructuredContent.(map[string]any)
			if !ok {
				t.Fatalf("structured content type %T", result.StructuredContent)
			}
			if content["approved"] != (phase == "record") {
				t.Fatalf("unexpected approval: %+v", content)
			}
			_ = session.Close()
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			if engine != nil && len(engine.Snapshot().AppliedFaults) != 1 {
				t.Fatal("SDK call missed fault")
			}
		})
	}
}
