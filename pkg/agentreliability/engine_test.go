package agentreliability

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

func testConfig() Config {
	return Config{
		Version:  1,
		Enabled:  true,
		Adapters: Adapters{MCP: true},
		Effects: []Effect{{
			Name: "payment.refund", Select: Selector{Tool: "payment.refund"},
			Identity: []string{"$.arguments.payment_id"},
		}},
		Faults: []Fault{{
			ID: "lost", Select: Selector{Kind: "mcp_tool_result", Tool: "payment.refund", Occurrence: 1},
			CommittedResponseLost: true,
		}, {
			ID: "missing", Select: Selector{Kind: "mcp_tool_result", Tool: "policy.check"},
			Mutations: []Mutation{{Operation: "delete", Path: "$.result.structuredContent.refundable"}},
		}},
		Assertions: []Assertion{{ID: "once", Type: "exactly_once_effect", Effect: "payment.refund"}, {
			ID: "verify", Type: "require_verification_before_retry", Effect: "payment.refund", VerificationTool: "payment.refund_status",
		}},
	}
}

func mcpRequest(tool, id string) Request {
	return Request{
		Method: http.MethodPost, Host: "mcp.test", Path: "/mcp", Headers: make(http.Header),
		Body: []byte(`{"jsonrpc":"2.0","id":"` + id + `","method":"tools/call","params":{"name":"` + tool + `","arguments":{"payment_id":"pay_1"}}}`),
	}
}

func TestCommittedResponseLostAndAssertions(t *testing.T) {
	config := testConfig()
	engine, err := NewEngine(config, []string{"lost"})
	if err != nil {
		t.Fatal(err)
	}
	response := Response{Status: 200, Headers: make(http.Header), Body: []byte(`{"jsonrpc":"2.0","id":"1","result":{"content":[{"type":"text","text":"ok"}]}}`)}
	decision, err := engine.Process(mcpRequest("payment.refund", "1"), response)
	if err != nil {
		t.Fatal(err)
	}
	if !decision.ResponseLost || len(decision.AppliedFaults) != 1 {
		t.Fatalf("decision = %#v", decision)
	}
	if _, err := engine.Process(mcpRequest("payment.refund", "2"), response); err != nil {
		t.Fatal(err)
	}
	results := Evaluate(config, engine.Snapshot(), false, time.Second)
	if results[0].Passed || results[1].Passed {
		t.Fatalf("expected unsafe retry to fail assertions: %#v", results)
	}
}

func TestVerificationBeforeRetryPasses(t *testing.T) {
	config := testConfig()
	engine, _ := NewEngine(config, []string{"lost"})
	response := Response{Status: 200, Headers: make(http.Header), Body: []byte(`{"jsonrpc":"2.0","id":"1","result":{}}`)}
	_, _ = engine.Process(mcpRequest("payment.refund", "1"), response)
	_, _ = engine.Process(mcpRequest("payment.refund_status", "2"), response)
	_, _ = engine.Process(mcpRequest("payment.refund", "3"), response)
	results := Evaluate(config, engine.Snapshot(), false, time.Second)
	if !results[1].Passed {
		t.Fatalf("verification assertion = %#v", results[1])
	}
}

func TestVerificationPredicateMustBeSatisfied(t *testing.T) {
	config := testConfig()
	config.Assertions[1].VerificationPath = "$.result.structuredContent.status"
	config.Assertions[1].VerificationValue = "refunded"
	engine, err := NewEngine(config, []string{"lost"})
	if err != nil {
		t.Fatal(err)
	}
	response := Response{Status: 200, Headers: make(http.Header), Body: []byte(`{"jsonrpc":"2.0","id":"1","result":{}}`)}
	_, _ = engine.Process(mcpRequest("payment.refund", "1"), response)
	_, _ = engine.Process(mcpRequest("payment.refund_status", "2"), Response{
		Status: 200, Headers: make(http.Header),
		Body: []byte(`{"jsonrpc":"2.0","id":"2","result":{"structuredContent":{"status":"pending"}}}`),
	})
	_, _ = engine.Process(mcpRequest("payment.refund", "3"), response)
	results := Evaluate(config, engine.Snapshot(), false, time.Second)
	if results[1].Passed {
		t.Fatalf("wrong verification response passed: %#v", results[1])
	}
}

func TestSemanticMutation(t *testing.T) {
	config := testConfig()
	engine, _ := NewEngine(config, []string{"missing"})
	response := Response{Status: 200, Headers: make(http.Header), Body: []byte(`{"jsonrpc":"2.0","id":"1","result":{"structuredContent":{"refundable":true}}}`)}
	decision, err := engine.Process(mcpRequest("policy.check", "1"), response)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(decision.Response.Body), "refundable") {
		t.Fatalf("mutation was not applied: %s", decision.Response.Body)
	}
}

func TestSSESemanticMutation(t *testing.T) {
	config := Config{
		Enabled:  true,
		Adapters: Adapters{Providers: []ProviderAdapter{{Name: "openai", Type: "openai", HostRegex: `^llm\.test$`}}},
		Faults: []Fault{{
			ID: "corrupt-stream", Select: Selector{Kind: "llm_response", Provider: "openai"},
			Mutations: []Mutation{{Operation: "delete", Path: "$.arguments.customer_id"}},
		}},
	}
	engine, err := NewEngine(config, []string{"corrupt-stream"})
	if err != nil {
		t.Fatal(err)
	}
	decision, err := engine.Process(Request{Method: "POST", Host: "llm.test", Path: "/v1/responses", Headers: make(http.Header), Body: []byte(`{}`)}, Response{
		Status: 200, Headers: http.Header{"Content-Type": {"text/event-stream"}},
		Body: []byte("event: response.output_item.added\ndata: {\"arguments\":{\"customer_id\":\"cust_1\",\"amount\":10}}\n\ndata: [DONE]\n\n"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(decision.Response.Body), "customer_id") || !strings.Contains(string(decision.Response.Body), "amount") {
		t.Fatalf("mutated SSE = %s", decision.Response.Body)
	}
}

func TestProviderEnvelopeRecognition(t *testing.T) {
	tests := []struct {
		providerType string
		body         string
		tool         string
	}{
		{"openai", `{"output":[{"type":"function_call","name":"payment.refund","call_id":"call_1"}]}`, "payment.refund"},
		{"anthropic", `{"content":[{"type":"tool_use","name":"policy.check","id":"tool_1"}]}`, "policy.check"},
		{"ollama", `{"message":{"tool_calls":[{"id":"tool_2","function":{"name":"inventory.lookup","arguments":{"sku":"1"}}}]}}`, "inventory.lookup"},
	}
	for _, test := range tests {
		t.Run(test.providerType, func(t *testing.T) {
			config := Config{Enabled: true, Adapters: Adapters{Providers: []ProviderAdapter{{Name: test.providerType, Type: test.providerType, HostRegex: "^provider.test$"}}}}
			engine, err := NewEngine(config, nil)
			if err != nil {
				t.Fatal(err)
			}
			_, err = engine.Process(Request{Method: "POST", Host: "provider.test", Path: "/v1", Headers: make(http.Header), Body: []byte(`{}`)}, Response{Status: 200, Headers: make(http.Header), Body: []byte(test.body)})
			if err != nil {
				t.Fatal(err)
			}
			snapshot := engine.Snapshot()
			if len(snapshot.Calls) != 1 || snapshot.Calls[0].Tool != test.tool {
				t.Fatalf("calls = %#v", snapshot.Calls)
			}
		})
	}
}

func TestConfigValidationAndStableCases(t *testing.T) {
	config := testConfig()
	if err := config.Validate(); err != nil {
		t.Fatal(err)
	}
	first, err := PlanCases(config, "incident", 42, true, 10)
	if err != nil {
		t.Fatal(err)
	}
	second, _ := PlanCases(config, "incident", 42, true, 10)
	if StableHash(first) != StableHash(second) || first[0].ID == first[1].ID {
		t.Fatalf("unstable cases: %#v %#v", first, second)
	}
	initial, err := PlanCases(config, "incident", 0, true, 2)
	if err != nil {
		t.Fatal(err)
	}
	foundDifferentSubset := false
	for seed := int64(1); seed < 100; seed++ {
		candidate, planErr := PlanCases(config, "incident", seed, true, 2)
		if planErr != nil {
			t.Fatal(planErr)
		}
		if candidate[1].FaultID != initial[1].FaultID {
			foundDifferentSubset = true
			break
		}
	}
	if !foundDifferentSubset {
		t.Fatal("seed did not change the deterministic budgeted fault subset")
	}
}

func TestConfigRejectsUnsafeOrAmbiguousRules(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Config)
	}{
		{"unknown provider", func(config *Config) {
			config.Adapters.Providers = []ProviderAdapter{{Name: "x", Type: "mystery", HostRegex: "x"}}
		}},
		{"unknown category", func(config *Config) {
			config.Faults[0].Category = "marketing_score"
		}},
		{"duplicate effect", func(config *Config) {
			config.Effects = append(config.Effects, config.Effects[0])
		}},
		{"ambiguous terminal fault", func(config *Config) {
			config.Faults[0].Reset = true
		}},
		{"lost response without effect", func(config *Config) {
			config.Faults[0].Select.Tool = "undeclared.tool"
		}},
		{"unsupported JSONPath", func(config *Config) {
			config.Faults[1].Mutations[0].Path = "$.result[*]"
		}},
		{"unknown assertion fault", func(config *Config) {
			config.Assertions[0].Faults = []string{"not-configured"}
		}},
		{"unbounded calls", func(config *Config) {
			config.Limits.MaxCalls = MaximumCalls + 1
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := testConfig()
			test.mutate(&config)
			if err := config.Validate(); err == nil {
				t.Fatal("expected validation failure")
			}
		})
	}
}

func FuzzEngineProcess(f *testing.F) {
	f.Add(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"policy.check","arguments":{}}}`, `{"jsonrpc":"2.0","id":1,"result":{"structuredContent":{"refundable":true}}}`)
	f.Fuzz(func(t *testing.T, requestBody, responseBody string) {
		config := testConfig()
		engine, err := NewEngine(config, []string{"missing"})
		if err != nil {
			t.Fatal(err)
		}
		_, _ = engine.Process(Request{Method: "POST", Host: "mcp.test", Path: "/mcp", Headers: make(http.Header), Body: []byte(requestBody)}, Response{Status: 200, Headers: make(http.Header), Body: []byte(responseBody)})
	})
}
