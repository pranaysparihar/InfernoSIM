package mcpproxy

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"infernosim/pkg/agentreliability"
	"infernosim/pkg/causal"
)

func causalRecords(messages ...string) []Record {
	var records []Record
	for i, s := range messages {
		direction := "client_to_server"
		if strings.HasPrefix(s, "S ") {
			direction = "server_to_client"
		}
		records = append(records, Record{Version: 1, Sequence: int64(i + 1), Direction: direction, Message: json.RawMessage(s[2:])})
	}
	return records
}
func replayEngine(t *testing.T, records []Record, order []string) *agentreliability.Engine {
	t.Helper()
	xs, err := Exchanges(records)
	if err != nil {
		t.Fatal(err)
	}
	g, err := causal.Discover(xs, nil, causal.Declarations{Review: "fixture application has reviewed independent requests"})
	if err != nil {
		t.Fatal(err)
	}
	cfg := agentreliability.Config{Enabled: true, Adapters: agentreliability.Adapters{MCP: true}, Causal: &causal.Replay{Graph: g, Schedule: causal.NewSchedule(g, order), Timeout: "1s"}}
	e, err := agentreliability.NewEngine(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestCausalReplayReceivesWhileResponseIsWaiting(t *testing.T) {
	records := causalRecords(`C {"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"one"}}`, `C {"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"two"}}`, `S {"jsonrpc":"2.0","id":1,"result":{"value":"one"}}`, `S {"jsonrpc":"2.0","id":2,"result":{"value":"two"}}`)
	order := []string{"mcp_1:admit", "mcp_2:admit", "mcp_2:release", "mcp_1:release"}
	engine := replayEngine(t, records, order)
	server, client := net.Pipe()
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- ReplayCausal(ReplayOptions{Context: ctx, Stdin: server, Stdout: server, Engine: engine}, records)
	}()
	// Deliver in the opposite admission order. net.Pipe writes also prove the
	// reader does not get stuck behind the first worker's scheduled response.
	for _, s := range []string{`{"jsonrpc":"2.0","id":"B","method":"tools/call","params":{"name":"two"}}`, `{"jsonrpc":"2.0","id":"A","method":"tools/call","params":{"name":"one"}}`} {
		if _, err := fmt.Fprintln(client, s); err != nil {
			t.Fatal(err)
		}
	}
	r := bufio.NewReader(client)
	for _, id := range []string{"B", "A"} {
		line, err := r.ReadBytes('\n')
		if err != nil {
			t.Fatal(err)
		}
		value, _ := causal.Decode(line)
		if value.(map[string]any)["id"] != id {
			t.Fatalf("wrong response correlation: %s", line)
		}
	}
	_ = client.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !engine.Causal().Snapshot().Completed {
		t.Fatal("schedule incomplete")
	}
}

func TestCausalReplayCancellationRemapsRuntimeTarget(t *testing.T) {
	records := causalRecords(`C {"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"slow"}}`, `C {"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":1}}`)
	engine := replayEngine(t, records, []string{"mcp_1:admit", "mcp_2:admit", "mcp_2:release", "mcp_1:release"})
	input := io.NopCloser(strings.NewReader("{\"jsonrpc\":\"2.0\",\"id\":\"fresh\",\"method\":\"tools/call\",\"params\":{\"name\":\"slow\"}}\n{\"jsonrpc\":\"2.0\",\"method\":\"notifications/cancelled\",\"params\":{\"requestId\":\"fresh\"}}\n"))
	var output strings.Builder
	if err := ReplayCausal(ReplayOptions{Context: context.Background(), Stdin: input, Stdout: &output, Engine: engine}, records); err != nil {
		t.Fatal(err)
	}
	if output.Len() != 0 {
		t.Fatal("cancelled request or notification produced an invented response")
	}
	for _, call := range engine.Snapshot().Calls {
		if call.Tool == "notifications/cancelled" && call.CancelTarget != "1" {
			t.Fatal("cancel target drifted")
		}
	}
}

func TestCausalServerProgressPreservesNotification(t *testing.T) {
	records := causalRecords(`C {"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"one","_meta":{"progressToken":"p"}}}`, `S {"jsonrpc":"2.0","method":"notifications/progress","params":{"progressToken":"p","progress":1}}`, `S {"jsonrpc":"2.0","id":1,"result":{}}`)
	engine := replayEngine(t, records, []string{"mcp_1:admit", "mcp_2:admit", "mcp_2:release", "mcp_1:release"})
	var output strings.Builder
	if err := ReplayCausal(ReplayOptions{Context: context.Background(), Stdin: io.NopCloser(strings.NewReader(string(records[0].Message) + "\n")), Stdout: &output, Engine: engine}, records); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 2 || lines[0] != string(records[1].Message) {
		t.Fatalf("notification changed: %s", output.String())
	}
}

func TestCausalReplayMissingParticipantAndMalformedTranscript(t *testing.T) {
	records := causalRecords(`C {"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"one"}}`, `S {"jsonrpc":"2.0","id":1,"result":{}}`)
	engine := replayEngine(t, records, []string{"mcp_1:admit", "mcp_1:release"})
	if err := ReplayCausal(ReplayOptions{Context: context.Background(), Stdin: io.NopCloser(strings.NewReader("")), Stdout: io.Discard, Engine: engine}, records); err == nil {
		t.Fatal("missing participant passed")
	}
	for _, bad := range [][]Record{records[:1], append(records, records[1]), causalRecords(`S {"jsonrpc":"2.0","id":1,"method":"sampling/createMessage"}`)} {
		if _, err := Exchanges(bad); err == nil {
			t.Fatal("accepted incomplete/duplicate/server-initiated transcript")
		}
	}
}
