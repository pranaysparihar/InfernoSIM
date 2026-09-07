package mcpproxy

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"infernosim/pkg/agentreliability"
)

func recordsFromMessages(messages ...string) []Record {
	var records []Record
	for i, message := range messages {
		direction, body, _ := strings.Cut(message, " ")
		records = append(records, Record{Version: 1, Sequence: int64(i + 1), Direction: direction, Message: json.RawMessage(body)})
	}
	return records
}

func TestParallelResponsesKeepRequestIdentity(t *testing.T) {
	records := recordsFromMessages(
		`client_to_server {"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"first","arguments":{}}}`,
		`client_to_server {"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"second","arguments":{}}}`,
		`server_to_client {"jsonrpc":"2.0","id":2,"result":{"value":"second"}}`,
		`server_to_client {"jsonrpc":"2.0","id":1,"result":{"value":"first"}}`,
	)
	path := writeTranscript(t, records)
	input := strings.ReplaceAll(string(records[0].Message), `"id":1`, `"id":"runtime-a"`) + "\n" + strings.ReplaceAll(string(records[1].Message), `"id":2`, `"id":"runtime-b"`) + "\n"
	c := agentreliability.Config{Adapters: agentreliability.Adapters{MCP: true}, Faults: []agentreliability.Fault{{ID: "first-only", Select: agentreliability.Selector{Tool: "first"}, Mutations: []agentreliability.Mutation{{Operation: "set", Path: "$.result.value", Value: "changed"}}}}}
	e, err := agentreliability.NewEngine(c, []string{"first-only"})
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := Replay(ReplayOptions{TranscriptPath: path, Stdin: strings.NewReader(input), Stdout: &out, Engine: e}); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], `"id":"runtime-b"`) || !strings.Contains(lines[0], "second") || !strings.Contains(lines[1], `"id":"runtime-a"`) || !strings.Contains(lines[1], "changed") {
		t.Fatal(out.String())
	}
	if e.Snapshot().Calls[0].Tool != "second" || e.Snapshot().Calls[1].Tool != "first" {
		t.Fatal("wrong fault target")
	}
}

func TestCancellationIDsAndToolListChangeNotification(t *testing.T) {
	records := recordsFromMessages(
		`client_to_server {"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"slow","arguments":{}}}`,
		`client_to_server {"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":1}}`,
		`server_to_client {"jsonrpc":"2.0","method":"notifications/tools/list_changed"}`,
		`server_to_client {"jsonrpc":"2.0","id":1,"error":{"code":-32603,"message":"cancelled"}}`,
		`client_to_server {"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`server_to_client {"jsonrpc":"2.0","id":2,"result":{"tools":[]}}`,
	)
	input := strings.ReplaceAll(string(records[0].Message), `"id":1`, `"id":99`) + "\n" + strings.ReplaceAll(string(records[1].Message), `"requestId":1`, `"requestId":99`) + "\n" + string(records[4].Message) + "\n"
	e, _ := agentreliability.NewEngine(agentreliability.Config{Adapters: agentreliability.Adapters{MCP: true}, Assertions: []agentreliability.Assertion{{ID: "cancel", Type: "no_calls_after_cancel", Tool: "slow"}}}, nil)
	var out bytes.Buffer
	if err := Replay(ReplayOptions{TranscriptPath: writeTranscript(t, records), Stdin: strings.NewReader(input), Stdout: &out, Engine: e}); err != nil {
		t.Fatal(err)
	}
	if strings.Count(out.String(), "list_changed") != 1 || !strings.Contains(out.String(), `"id":99`) {
		t.Fatal(out.String())
	}
	if e.Snapshot().Calls[0].CancelTarget != "99" {
		t.Fatal("cancellation missing from evidence")
	}
	if !agentreliability.Evaluate(agentreliability.Config{Assertions: []agentreliability.Assertion{{ID: "cancel", Type: "no_calls_after_cancel", Tool: "slow"}}}, e.Snapshot(), false, 0)[0].Passed {
		t.Fatal("already admitted work was misclassified as a new call after cancellation")
	}
}

func TestIncompleteTranscriptAndDuplicateRuntimeIDsFail(t *testing.T) {
	records := recordsFromMessages(
		`client_to_server {"id":1,"method":"tools/list"}`,
		`client_to_server {"id":2,"method":"tools/list"}`,
		`server_to_client {"id":1,"result":{"tools":[]}}`,
		`server_to_client {"id":2,"result":{"tools":[]}}`,
	)
	for _, input := range []string{string(records[0].Message) + "\n", string(records[0].Message) + "\n" + string(records[0].Message) + "\n"} {
		if err := Replay(ReplayOptions{TranscriptPath: writeTranscript(t, records), Stdin: strings.NewReader(input), Stdout: &bytes.Buffer{}}); err == nil {
			t.Fatal("invalid replay passed")
		}
	}
}
