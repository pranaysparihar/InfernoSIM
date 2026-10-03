package agentreliability

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
)

func okResponse() Response {
	return Response{Status: 200, Headers: make(http.Header), Body: []byte(`{"result":{"approved":true,"healthy":true}}`)}
}

func TestPairwisePlanAndTransportComposition(t *testing.T) {
	c := testConfig()
	c.Exploration.Pairwise = true
	c.Faults[1] = Fault{ID: "second", Select: Selector{Tool: "payment.refund"}, Mutations: []Mutation{{Operation: "set", Path: "$.result.healthy", Value: false}}}
	cases, err := PlanCases(c, "scope", 42, true, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(cases) != 4 || len(cases[3].ActiveFaults()) != 2 || cases[3].Baseline() {
		t.Fatalf("cases=%+v", cases)
	}
	repeat, _ := PlanCases(c, "scope", 42, true, 10)
	if StableHash(cases) != StableHash(repeat) {
		t.Fatal("unstable plan")
	}
	e, err := NewEngine(c, []string{"lost", "second"})
	if err != nil {
		t.Fatal(err)
	}
	d, err := e.Process(mcpRequest("payment.refund", "1"), okResponse())
	if err != nil {
		t.Fatal(err)
	}
	if !d.ResponseLost || len(d.AppliedFaults) != 2 {
		t.Fatalf("transport fault overwritten: %+v", d)
	}
	limited, _ := PlanCases(c, "scope", 42, true, 2)
	if len(limited) != 2 {
		t.Fatal("budget exceeded")
	}
}

func TestParallelScheduleReproducesAdmission(t *testing.T) {
	for repeat := 0; repeat < 30; repeat++ {
		c := testConfig()
		c.Schedules = []Schedule{{ID: "reverse", Timeout: "1s", Steps: []Selector{{CallID: "b"}, {CallID: "a"}}}}
		e, err := NewEngine(c, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := e.SetSchedule("reverse"); err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		for _, id := range []string{"a", "b"} {
			wg.Add(1)
			go func(id string) {
				defer wg.Done()
				if _, err := e.Process(mcpRequest("lookup", id), okResponse()); err != nil {
					t.Error(err)
				}
			}(id)
		}
		wg.Wait()
		s := e.Snapshot()
		if len(s.Calls) != 2 || s.Calls[0].CallID != "b" || s.Calls[1].CallID != "a" {
			t.Fatalf("wrong order: %+v", s)
		}
		results := Evaluate(c, s, false, 0)
		if !results[len(results)-1].Passed {
			t.Fatal("complete schedule failed")
		}
	}
}

func TestScheduleMissingParticipantAndUnexpectedCallFailClosed(t *testing.T) {
	for _, id := range []string{"b", "unknown"} {
		t.Run(id, func(t *testing.T) {
			c := testConfig()
			c.Schedules = []Schedule{{ID: "s", Timeout: "5ms", Steps: []Selector{{CallID: "a"}, {CallID: "b"}}}}
			e, _ := NewEngine(c, nil)
			_ = e.SetSchedule("s")
			if _, err := e.ProcessContext(context.Background(), mcpRequest("lookup", id), okResponse()); err == nil {
				t.Fatal("missing schedule participant passed")
			}
			if !e.Snapshot().ScheduleFailed || len(e.Snapshot().Calls) != 0 {
				t.Fatal("unexpected admission")
			}
		})
	}
}

func TestEveryProviderToolCanBeSelected(t *testing.T) {
	for _, body := range []string{
		`{"output":[{"type":"function_call","name":"first","call_id":"a"},{"type":"function_call","name":"second","call_id":"b"}]}`,
		`{"choices":[{"message":{"tool_calls":[{"id":"a","function":{"name":"first"}},{"id":"b","function":{"name":"second"}}]}}]}`,
		`{"content":[{"type":"tool_use","name":"first","id":"a"},{"type":"tool_use","name":"second","id":"b"}]}`,
		`{"message":{"tool_calls":[{"id":"a","function":{"name":"first"}},{"id":"b","function":{"name":"second"}}]}}`,
	} {
		c := Config{Adapters: Adapters{Providers: []ProviderAdapter{{Name: "provider", Type: "generic", HostRegex: "provider"}}}, Faults: []Fault{{ID: "second", Select: Selector{Tool: "second", CallID: "b"}, Status: 503}}}
		e, err := NewEngine(c, []string{"second"})
		if err != nil {
			t.Fatal(err)
		}
		d, err := e.Process(Request{Host: "provider"}, Response{Status: 200, Body: []byte(body)})
		if err != nil {
			t.Fatal(err)
		}
		if d.Response.Status != 503 || len(e.Snapshot().Calls[0].Operations) != 2 {
			t.Fatal("second tool not recognized")
		}
	}
}

func TestSafetyCoverageIsNotVacuous(t *testing.T) {
	c := testConfig()
	c.Assertions = c.Assertions[1:]
	c.Assertions[0].RequireExercised = true
	r := Evaluate(c, Snapshot{}, false, 0)[0]
	if r.Passed || r.Coverage != "not_exercised" {
		t.Fatalf("unexercised guard passed: %+v", r)
	}
	c.Assertions[0].RequireExercised = false
	r = Evaluate(c, Snapshot{}, false, 0)[0]
	if !r.Passed || r.Coverage != "not_exercised" {
		t.Fatal(r)
	}
}

func TestApprovalIntegrity(t *testing.T) {
	for _, mode := range []string{"safe", "changed-tenant", "changed-amount", "missing", "expired", "reused", "denied", "error", "missing-binding"} {
		t.Run(mode, func(t *testing.T) {
			c := testConfig()
			c.Assertions = []Assertion{{ID: "approve", Type: "approval_before_effect", Effect: "payment.refund", VerificationTool: "approve", VerificationPath: "$.result.approved", VerificationValue: true, Bind: []string{"$.arguments.payment_id", "$.arguments.tenant", "$.arguments.amount"}, MaxAgeCalls: 2, RequireExercised: true}}
			e, err := NewEngine(c, nil)
			if err != nil {
				t.Fatal(err)
			}
			request := func(tool, id, tenant string, amount int) Request {
				r := mcpRequest(tool, id)
				r.Body = []byte(`{"id":"` + id + `","method":"tools/call","params":{"name":"` + tool + `","arguments":{"payment_id":"p","tenant":"` + tenant + `","amount":` + scalarString(amount) + `}}}`)
				return r
			}
			approve := okResponse()
			if mode == "denied" {
				approve.Body = []byte(`{"result":{"approved":false}}`)
			}
			if mode == "error" {
				approve.Body = []byte(`{"error":{"message":"failed"},"result":{"approved":true}}`)
			}
			if mode != "missing" {
				_, err = e.Process(request("approve", "1", "a", 10), approve)
				if err != nil {
					t.Fatal(err)
				}
			}
			if mode == "expired" {
				for _, id := range []string{"2", "3"} {
					_, _ = e.Process(mcpRequest("lookup", id), okResponse())
				}
			}
			tenant := "a"
			amount := 10
			if mode == "changed-tenant" {
				tenant = "b"
			}
			if mode == "changed-amount" {
				amount = 11
			}
			req := request("payment.refund", "4", tenant, amount)
			if mode == "missing-binding" {
				req = mcpRequest("payment.refund", "4")
			}
			_, err = e.Process(req, okResponse())
			if err != nil {
				t.Fatal(err)
			}
			if mode == "reused" {
				_, _ = e.Process(req, okResponse())
			}
			r := Evaluate(c, e.Snapshot(), false, 0)[0]
			if r.Passed != (mode == "safe") {
				t.Fatalf("mode=%s result=%+v", mode, r)
			}
			encoded, _ := json.Marshal(e.Snapshot())
			if strings.Contains(string(encoded), `"amount"`) || strings.Contains(string(encoded), `"tenant"`) {
				t.Fatal("raw arguments leaked")
			}
		})
	}
}

func TestMonitorGroundTruthAndMissingTelemetry(t *testing.T) {
	for _, mode := range []string{"clean", "false-healthy", "missing", "alert"} {
		t.Run(mode, func(t *testing.T) {
			c := testConfig()
			c.Assertions = []Assertion{{ID: "monitor", Type: "monitor_sound", Effect: "payment.refund", Tool: "monitor", Path: "$.result.healthy", Expected: true, RequireExercised: true}}
			e, err := NewEngine(c, nil)
			if err != nil {
				t.Fatal(err)
			}
			if mode != "clean" {
				_, _ = e.Process(mcpRequest("payment.refund", "1"), okResponse())
			}
			r := okResponse()
			if mode == "missing" {
				r.Body = []byte(`{"result":{}}`)
			}
			if mode == "alert" {
				r.Body = []byte(`{"result":{"healthy":false}}`)
			}
			_, _ = e.Process(mcpRequest("monitor", "2"), r)
			result := Evaluate(c, e.Snapshot(), false, 0)[0]
			if result.Passed != (mode == "clean" || mode == "alert") {
				t.Fatal(result)
			}
		})
	}
}

func TestCompensationRequiresMatchingLaterUnusedEffect(t *testing.T) {
	c := testConfig()
	c.Effects = append(c.Effects, Effect{Name: "undo", Select: Selector{Tool: "undo"}, Identity: []string{"$.arguments.payment_id"}})
	c.Assertions = []Assertion{{ID: "comp", Type: "compensated_effect", Effect: "payment.refund", Compensation: "undo"}}
	for _, mode := range []string{"safe", "early", "missing", "duplicate", "wrong-identity"} {
		t.Run(mode, func(t *testing.T) {
			e, err := NewEngine(c, nil)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "early" {
				_, _ = e.Process(mcpRequest("undo", "0"), okResponse())
			}
			_, _ = e.Process(mcpRequest("payment.refund", "1"), okResponse())
			if mode == "duplicate" {
				_, _ = e.Process(mcpRequest("payment.refund", "2"), okResponse())
			}
			if mode != "missing" && mode != "early" {
				req := mcpRequest("undo", "3")
				if mode == "wrong-identity" {
					req.Body = []byte(strings.ReplaceAll(string(req.Body), "pay_1", "pay_2"))
				}
				_, _ = e.Process(req, okResponse())
			}
			if r := Evaluate(c, e.Snapshot(), false, 0)[0]; r.Passed != (mode == "safe") {
				t.Fatal(r)
			}
		})
	}
}

func TestTokenBudgetUnknownUsageFails(t *testing.T) {
	for _, body := range []string{`{"usage":{"total_tokens":4}}`, `{"usage":{"input_tokens":2,"output_tokens":2}}`, `{"prompt_eval_count":2,"eval_count":2}`, `{}`, `{"usage":{"total_tokens":-1}}`, `{"usage":{"total_tokens":4.5}}`, `{"usage":{"total_tokens":6}}`} {
		c := Config{Adapters: Adapters{Providers: []ProviderAdapter{{Name: "p", Type: "generic", HostRegex: "p"}}}, Assertions: []Assertion{{ID: "tokens", Type: "max_tokens", Max: 5}, {ID: "calls", Type: "max_total_calls", Max: 0}}}
		e, err := NewEngine(c, nil)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = e.Process(Request{Host: "p"}, Response{Status: 200, Body: []byte(body)})
		r := Evaluate(c, e.Snapshot(), false, 0)
		want := body == `{"usage":{"total_tokens":4}}` || strings.Contains(body, "input_tokens") || strings.Contains(body, "prompt_eval_count")
		if r[0].Passed != want || r[1].Passed {
			t.Fatalf("%s => %+v", body, r)
		}
	}
}

func TestCancellationAndIsolation(t *testing.T) {
	c := testConfig()
	c.Assertions = []Assertion{{ID: "cancel", Type: "no_calls_after_cancel", Tool: "payment.refund"}, {ID: "tenant", Type: "request_matches", Tool: "payment.refund", Path: "$.headers.x-tenant", Expected: "a"}}
	e, err := NewEngine(c, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = e.Process(Request{Body: []byte(`{"method":"notifications/cancelled","params":{"requestId":"x"}}`)}, Response{Status: 204})
	r := mcpRequest("payment.refund", "x")
	r.Headers.Set("X-Tenant", "b")
	_, _ = e.Process(r, okResponse())
	results := Evaluate(c, e.Snapshot(), false, 0)
	if results[0].Passed || results[1].Passed {
		t.Fatal(results)
	}
}

func TestSnapshotDoesNotExposeMutableEvidence(t *testing.T) {
	c := testConfig()
	e, _ := NewEngine(c, nil)
	_, _ = e.Process(mcpRequest("lookup", "1"), okResponse())
	s := e.Snapshot()
	s.Calls[0].Checks = map[string]bool{"tampered": true}
	if e.Snapshot().Calls[0].Checks["tampered"] {
		t.Fatal("mutable snapshot")
	}
}

func TestCombinedDelaysRemainBounded(t *testing.T) {
	c := Config{Faults: []Fault{{ID: "a", Delay: "40s"}, {ID: "b", Delay: "40s"}}}
	e, err := NewEngine(c, []string{"a", "b"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Process(Request{}, okResponse()); err == nil {
		t.Fatal("unbounded combined delay")
	}
}

func TestInvalidSafetyConfiguration(t *testing.T) {
	for _, a := range []Assertion{{ID: "a", Type: "approval_before_effect", Effect: "payment.refund"}, {ID: "a", Type: "request_matches", Tool: "x"}, {ID: "a", Type: "max_tokens", Max: -1}, {ID: "a", Type: "compensated_effect", Effect: "payment.refund", Compensation: "missing"}} {
		c := testConfig()
		c.Assertions = []Assertion{a}
		if c.Validate() == nil {
			t.Fatalf("accepted %+v", a)
		}
	}
}

func TestLargeIntegerIdentityAndMissingDeduplication(t *testing.T) {
	c := testConfig()
	c.Effects[0].DeduplicateBy = []string{"$.arguments.payment_id"}
	e, err := NewEngine(c, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, number := range []string{"9007199254740992", "9007199254740993"} {
		r := mcpRequest("payment.refund", "x")
		r.Body = []byte(strings.ReplaceAll(string(r.Body), `"pay_1"`, number))
		if _, err := e.Process(r, okResponse()); err != nil {
			t.Fatal(err)
		}
	}
	s := e.Snapshot()
	if len(s.Effects) != 2 || !s.Effects[0].Committed || !s.Effects[1].Committed || s.Effects[0].IdentityHash == s.Effects[1].IdentityHash {
		t.Fatal("integer identities were rounded together")
	}
	r := mcpRequest("payment.refund", "missing")
	r.Body = []byte(strings.ReplaceAll(string(r.Body), `{"payment_id":"pay_1"}`, `{}`))
	if _, err := e.Process(r, okResponse()); err == nil {
		t.Fatal("missing deduplication key accepted")
	}
}

func TestUsageTamperingCannotEraseBudgetGroundTruth(t *testing.T) {
	c := Config{Adapters: Adapters{Providers: []ProviderAdapter{{Name: "p", Type: "generic", HostRegex: "p"}}}, Faults: []Fault{{ID: "hide-usage", Mutations: []Mutation{{Operation: "set", Path: "$.usage.total_tokens", Value: 0}}}}, Assertions: []Assertion{{ID: "cost", Type: "max_cost", Max: 10, PricePerTokenMicrounits: 2}}}
	e, err := NewEngine(c, []string{"hide-usage"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Process(Request{Host: "p"}, Response{Status: 200, Body: []byte(`{"usage":{"total_tokens":6}}`)}); err != nil {
		t.Fatal(err)
	}
	r := Evaluate(c, e.Snapshot(), false, 0)[0]
	if r.Passed || e.Snapshot().Calls[0].Tokens != 6 {
		t.Fatalf("budget trusted tampered telemetry: %+v", r)
	}
}

func TestMonitorRevisionAndEstimateChecks(t *testing.T) {
	for _, version := range []int{0, 1, 2} {
		c := testConfig()
		c.Assertions = []Assertion{{ID: "m", Type: "monitor_sound", Effect: "payment.refund", Tool: "monitor", Path: "$.result.healthy", Expected: true, VersionPath: "$.result.version", EstimatePath: "$.arguments.version"}}
		e, err := NewEngine(c, nil)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = e.Process(mcpRequest("payment.refund", "1"), okResponse())
		r := mcpRequest("monitor", "2")
		r.Body = []byte(strings.ReplaceAll(string(r.Body), `"payment_id":"pay_1"`, `"payment_id":"pay_1","version":`+scalarString(version)))
		_, err = e.Process(r, Response{Status: 200, Body: []byte(`{"result":{"healthy":false,"version":1}}`)})
		if err != nil {
			t.Fatal(err)
		}
		if result := Evaluate(c, e.Snapshot(), false, 0)[0]; result.Passed != (version == 1) {
			t.Fatal(result)
		}
	}
}

func TestPartialScheduleRetainsOrderAndRequiresSelectedCalls(t *testing.T) {
	c := testConfig()
	c.Schedules = []Schedule{{ID: "partial", Timeout: "1s", Partial: true, Steps: []Selector{{CallID: "b"}, {CallID: "a"}}}}
	e, err := NewEngine(c, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = e.SetSchedule("partial"); err != nil {
		t.Fatal(err)
	}
	if _, err = e.Process(mcpRequest("lookup", "unlisted"), okResponse()); err != nil {
		t.Fatal(err)
	}
	if e.Snapshot().ScheduleCompleted != 0 {
		t.Fatal("unlisted call advanced schedule")
	}
	var wg sync.WaitGroup
	for _, id := range []string{"a", "b"} {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			if _, err := e.Process(mcpRequest("lookup", id), okResponse()); err != nil {
				t.Error(err)
			}
		}(id)
	}
	wg.Wait()
	if _, err = e.Process(mcpRequest("lookup", "after"), okResponse()); err != nil {
		t.Fatal(err)
	}
	s := e.Snapshot()
	if len(s.Calls) != 4 || s.Calls[1].CallID != "b" || s.Calls[2].CallID != "a" || s.ScheduleCompleted != 2 {
		t.Fatalf("%+v", s)
	}
	e, _ = NewEngine(c, nil)
	_ = e.SetSchedule("partial")
	_, _ = e.Process(mcpRequest("lookup", "unlisted"), okResponse())
	results := Evaluate(c, e.Snapshot(), false, 0)
	if results[len(results)-1].Passed {
		t.Fatal("missing required calls passed partial schedule")
	}
}
