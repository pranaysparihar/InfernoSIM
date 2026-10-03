package mcpproxy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"infernosim/pkg/agentreliability"
)

func TestRecordCommandCapturesBothDirections(t *testing.T) {
	t.Setenv("GO_WANT_MCP_RECORD_HELPER", "1")
	path := filepath.Join(t.TempDir(), "mcp.log")
	input := strings.NewReader(`{"jsonrpc":"2.0","id":7,"method":"tools/list"}` + "\n")
	var output bytes.Buffer
	err := RecordCommand(context.Background(), RecordOptions{
		OutputPath: path, Command: []string{os.Args[0], "-test.run=TestMCPRecordHelper", "--"},
		Stdin: input, Stdout: &output, Stderr: &bytes.Buffer{},
	})
	if err != nil {
		t.Fatal(err)
	}
	records, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 || records[0].Direction != "client_to_server" || records[1].Direction != "server_to_client" {
		t.Fatalf("records = %#v", records)
	}
	if !strings.Contains(output.String(), `"id":7`) {
		t.Fatalf("protocol output = %s", output.String())
	}
	if info, err := os.Stat(path); err != nil || (runtime.GOOS != "windows" && info.Mode().Perm() != 0o600) {
		t.Fatalf("transcript permissions = %v, %v", info, err)
	}
}

func TestRecordCommandDoesNotLeavePartialTranscript(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp.log")
	err := RecordCommand(context.Background(), RecordOptions{OutputPath: path, Command: []string{filepath.Join(t.TempDir(), "missing-command")}})
	if err == nil {
		t.Fatal("expected child start failure")
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatalf("partial transcript exists: %v", statErr)
	}
}

// A child can exit while its final response is still being delivered. Waiting
// for the child first closes StdoutPipe and loses the remaining read/EOF.
func TestRecordCommandDrainsOutputBeforeWait(t *testing.T) {
	t.Setenv("GO_WANT_MCP_RECORD_HELPER", "1")
	t.Setenv("GORACE", "atexit_sleep_ms=0")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	output := &delayedRecordOutput{}
	path := filepath.Join(t.TempDir(), "mcp.log")
	err := RecordCommand(ctx, RecordOptions{
		OutputPath: path, Command: []string{os.Args[0], "-test.run=TestMCPRecordHelper", "--"},
		Stdin:  strings.NewReader(`{"jsonrpc":"2.0","id":7,"method":"tools/list"}` + "\n"),
		Stdout: output, Stderr: &bytes.Buffer{},
	})
	if err != nil {
		t.Fatal(err)
	}
	records, err := Load(path)
	if err != nil || len(records) != 2 || !strings.Contains(output.String(), `"id":7`) {
		t.Fatalf("records = %#v, output = %q, error = %v", records, output.String(), err)
	}
}

type delayedRecordOutput struct{ bytes.Buffer }

func (w *delayedRecordOutput) Write(p []byte) (int, error) {
	time.Sleep(100 * time.Millisecond)
	return w.Buffer.Write(p)
}

func TestMCPRecordHelper(t *testing.T) {
	if os.Getenv("GO_WANT_MCP_RECORD_HELPER") != "1" {
		return
	}
	scanner := bufio.NewScanner(os.Stdin)
	if !scanner.Scan() {
		os.Exit(31)
	}
	var request map[string]any
	if json.Unmarshal(scanner.Bytes(), &request) != nil {
		os.Exit(32)
	}
	fmt.Printf(`{"jsonrpc":"2.0","id":%v,"result":{"tools":[]}}`+"\n", request["id"])
	os.Exit(0)
}

func TestReplayRewritesIDsAndMutatesToolResult(t *testing.T) {
	path := writeTranscript(t, []Record{{
		Version: 1, Sequence: 1, Direction: "client_to_server",
		Message: json.RawMessage(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"policy.check","arguments":{}}}`),
	}, {
		Version: 1, Sequence: 2, Direction: "server_to_client",
		Message: json.RawMessage(`{"jsonrpc":"2.0","id":1,"result":{"structuredContent":{"refundable":true}}}`),
	}})
	config := agentreliability.Config{
		Enabled: true, Adapters: agentreliability.Adapters{MCP: true},
		Faults: []agentreliability.Fault{{ID: "missing", Select: agentreliability.Selector{Kind: "mcp_tool_result", Tool: "policy.check"}, Mutations: []agentreliability.Mutation{{Operation: "delete", Path: "$.result.structuredContent.refundable"}}}},
	}
	engine, err := agentreliability.NewEngine(config, []string{"missing"})
	if err != nil {
		t.Fatal(err)
	}
	input := strings.NewReader(`{"jsonrpc":"2.0","id":"runtime","method":"tools/call","params":{"name":"policy.check","arguments":{}}}` + "\n")
	var output bytes.Buffer
	if err := Replay(ReplayOptions{TranscriptPath: path, Stdin: input, Stdout: &output, Engine: engine}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), `"id":"runtime"`) || strings.Contains(output.String(), "refundable") {
		t.Fatalf("output = %s", output.String())
	}
}

func TestReplayRejectsDivergence(t *testing.T) {
	path := writeTranscript(t, []Record{{Version: 1, Sequence: 1, Direction: "client_to_server", Message: json.RawMessage(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)}})
	err := Replay(ReplayOptions{TranscriptPath: path, Stdin: strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"resources/list"}` + "\n"), Stdout: &bytes.Buffer{}})
	if err == nil || !strings.Contains(err.Error(), "divergence") {
		t.Fatalf("error = %v", err)
	}
}

func TestReplayDelayIsContextCancelable(t *testing.T) {
	path := writeTranscript(t, []Record{
		{Version: 1, Sequence: 1, Direction: "client_to_server", Message: json.RawMessage(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)},
		{Version: 1, Sequence: 2, Direction: "server_to_client", Message: json.RawMessage(`{"jsonrpc":"2.0","id":1,"result":{"tools":[]}}`)},
	})
	config := agentreliability.Config{Enabled: true, Adapters: agentreliability.Adapters{MCP: true}, Faults: []agentreliability.Fault{{
		ID: "slow", Select: agentreliability.Selector{Kind: "mcp_tools_list"}, Delay: "1s",
	}}}
	engine, err := agentreliability.NewEngine(config, []string{"slow"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	started := time.Now()
	err = Replay(ReplayOptions{
		Context: ctx, TranscriptPath: path,
		Stdin: strings.NewReader(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}` + "\n"), Stdout: &bytes.Buffer{}, Engine: engine,
	})
	if !errors.Is(err, context.Canceled) || time.Since(started) > 100*time.Millisecond {
		t.Fatalf("cancelled replay = %v after %s", err, time.Since(started))
	}
}

func TestReplayContinuesAfterCommittedResponseLoss(t *testing.T) {
	request := json.RawMessage(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"payment.refund","arguments":{"payment_id":"pay_1"}}}`)
	response := json.RawMessage(`{"jsonrpc":"2.0","id":1,"result":{"structuredContent":{"status":"refunded"}}}`)
	path := writeTranscript(t, []Record{
		{Version: 1, Sequence: 1, Direction: "client_to_server", Message: request},
		{Version: 1, Sequence: 2, Direction: "server_to_client", Message: response},
		{Version: 1, Sequence: 3, Direction: "client_to_server", Message: request},
		{Version: 1, Sequence: 4, Direction: "server_to_client", Message: response},
	})
	config := agentreliability.Config{
		Enabled: true, Adapters: agentreliability.Adapters{MCP: true},
		Effects: []agentreliability.Effect{{Name: "payment.refund", Select: agentreliability.Selector{Tool: "payment.refund"}, Identity: []string{"$.arguments.payment_id"}}},
		Faults: []agentreliability.Fault{{
			ID: "lost", Select: agentreliability.Selector{Kind: "mcp_tool_result", Tool: "payment.refund", Occurrence: 1}, CommittedResponseLost: true,
		}},
	}
	engine, err := agentreliability.NewEngine(config, []string{"lost"})
	if err != nil {
		t.Fatal(err)
	}
	input := strings.NewReader(string(request) + "\n" + strings.Replace(string(request), `"id":1`, `"id":2`, 1) + "\n")
	var output bytes.Buffer
	if err := Replay(ReplayOptions{TranscriptPath: path, Stdin: input, Stdout: &output, Engine: engine}); err != nil {
		t.Fatal(err)
	}
	if strings.Count(output.String(), `"status":"refunded"`) != 1 || !strings.Contains(output.String(), `"id":2`) {
		t.Fatalf("only the retry response should be delivered: %s", output.String())
	}
	snapshot := engine.Snapshot()
	if len(snapshot.Effects) != 2 || !snapshot.Effects[0].ResponseLost || snapshot.Effects[1].ResponseLost {
		t.Fatalf("effects = %#v", snapshot.Effects)
	}
}

func writeTranscript(t *testing.T, records []Record) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "mcp.log")
	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	for _, record := range records {
		if err := encoder.Encode(record); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(path, output.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func FuzzEquivalentRequest(f *testing.F) {
	f.Add(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	f.Fuzz(func(t *testing.T, left, right string) {
		_ = equivalentRequest([]byte(left), []byte(right))
	})
}
