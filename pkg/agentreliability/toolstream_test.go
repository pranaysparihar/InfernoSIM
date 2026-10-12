package agentreliability

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"infernosim/pkg/causal"
	"infernosim/pkg/toolstream"
)

func TestStreamedCallSelectorTargetsSecondParallelTool(t *testing.T) {
	for file, provider := range map[string]string{"responses.sse": "openai", "chat.sse": "openai", "anthropic.sse": "anthropic", "ollama.ndjson": "ollama"} {
		t.Run(file, func(t *testing.T) {
			body, err := os.ReadFile(filepath.Join("..", "toolstream", "testdata", file))
			if err != nil {
				t.Fatal(err)
			}
			id := "call_b"
			contentType := "text/event-stream"
			if provider == "ollama" {
				id = "ollama:1"
				contentType = "application/x-ndjson"
			}
			config := Config{Enabled: true, Adapters: Adapters{Providers: []ProviderAdapter{{Name: "model", Type: provider, HostRegex: "^llm.test$"}}}, Faults: []Fault{{ID: "bad-amount", Select: Selector{Kind: "llm_response", Provider: "model", Tool: "lookup", CallID: id}, Mutations: []Mutation{{Target: "tool_arguments", Operation: "set", Path: "$.amount", Value: 99}}}}}
			engine, err := NewEngine(config, []string{"bad-amount"})
			if err != nil {
				t.Fatal(err)
			}
			decision, err := engine.Process(Request{Method: "POST", Host: "llm.test", Body: []byte(`{}`)}, Response{Status: 200, Headers: http.Header{"Content-Type": {contentType}}, Body: body})
			if err != nil {
				t.Fatal(err)
			}
			stream, err := toolstream.Parse(provider, decision.Response.Body)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(stream.Calls[0].Arguments), `10`) || !strings.Contains(string(stream.Calls[1].Arguments), `99`) {
				t.Fatalf("wrong tool targeted: %+v", stream.Calls)
			}
			snapshot := engine.Snapshot()
			if len(snapshot.Calls[0].Operations) != 2 || !snapshot.Calls[0].UsageKnown || snapshot.Calls[0].Tokens != 30 {
				t.Fatalf("missing stream identities or usage: %+v", snapshot)
			}
			proof, _ := json.Marshal(snapshot)
			if strings.Contains(string(proof), "account") {
				t.Fatal("raw arguments leaked in proof")
			}
			config.Faults[0].Select.CallID = ""
			engine, err = NewEngine(config, []string{"bad-amount"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := engine.Process(Request{Host: "llm.test"}, Response{Status: 200, Headers: http.Header{"Content-Type": {contentType}}, Body: body}); err == nil {
				t.Fatal("ambiguous parallel tool selector mutated a random call")
			}
		})
	}
}

func TestApprovalMustBeReleasedBeforeConcurrentEffect(t *testing.T) {
	for _, early := range []bool{true, false} {
		config := testConfig()
		config.Assertions = []Assertion{{ID: "approval", Type: "approval_before_effect", Effect: "payment.refund", VerificationTool: "approve", VerificationPath: "$.result.approved", VerificationValue: true, Bind: []string{"$.arguments.payment_id"}, MaxAgeCalls: 2}}
		approval := mcpRequest("approve", "1")
		effect := mcpRequest("payment.refund", "2")
		xs := []causal.Exchange{{ID: "a", Protocol: "http", Record: "a", Request: approval.Body}, {ID: "b", Protocol: "http", Record: "b", Request: effect.Body}}
		graph, err := causal.Discover(xs, nil, causal.Declarations{Review: "fixture"})
		if err != nil {
			t.Fatal(err)
		}
		plan, err := causal.Generate(graph, 0, 1)
		if err != nil {
			t.Fatal(err)
		}
		config.Causal = &causal.Replay{Graph: graph, Schedule: plan.Schedules[0], Timeout: "1s"}
		engine, err := NewEngine(config, nil)
		if err != nil {
			t.Fatal(err)
		}
		decision, err := engine.Process(approval, okResponse())
		if err != nil {
			t.Fatal(err)
		}
		if early {
			engine.CompleteResponse(approval, decision, true)
		}
		other, err := engine.Process(effect, okResponse())
		if err != nil {
			t.Fatal(err)
		}
		engine.CompleteResponse(effect, other, true)
		if !early {
			engine.CompleteResponse(approval, decision, true)
		}
		result := Evaluate(config, engine.Snapshot(), false, 0)[0]
		if result.Passed != early {
			t.Fatalf("released early=%t assertion=%+v", early, result)
		}
	}
}
