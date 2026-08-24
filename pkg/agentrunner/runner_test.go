package agentrunner

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"infernosim/pkg/agentreliability"
	"infernosim/pkg/event"
	"infernosim/pkg/reporting"
)

func TestRunDetectsUnsafeAmbiguousRetry(t *testing.T) {
	incident := writeAgentIncident(t)
	prepared, err := Prepare(incident, "", 42, 10, true)
	if err != nil {
		t.Fatal(err)
	}
	lost, err := ResolveCase(prepared.Cases, "refund-response-lost")
	if err != nil {
		t.Fatal(err)
	}
	result, err := Run(context.Background(), Options{
		IncidentDir: incident, Case: lost,
		Command:     []string{os.Args[0], "-test.run=TestAgentRunnerHelper", "--"},
		Environment: []string{"GO_WANT_AGENT_HELPER=1", "AGENT_MODE=unsafe"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Passed || result.Failure == "" {
		t.Fatalf("unsafe result = %#v", result)
	}
}

func TestRunConfirmsSafeVerification(t *testing.T) {
	incident := writeAgentIncident(t)
	prepared, err := Prepare(incident, "", 42, 10, true)
	if err != nil {
		t.Fatal(err)
	}
	lost, _ := ResolveCase(prepared.Cases, "refund-response-lost")
	result, err := Run(context.Background(), Options{
		IncidentDir: incident, Case: lost,
		Command:     []string{os.Args[0], "-test.run=TestAgentRunnerHelper", "--"},
		Environment: []string{"GO_WANT_AGENT_HELPER=1", "AGENT_MODE=safe"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Passed {
		t.Fatalf("safe result failed: %s stderr=%s", result.Failure, result.Process.Stderr)
	}
	for _, assertion := range result.Assertions {
		if !assertion.Passed {
			t.Fatalf("assertion failed: %#v", assertion)
		}
	}
}

func TestReportingIncludesOneJUnitCasePerFaultCase(t *testing.T) {
	report := ReportingResult([]Result{{Case: caseValue("fc_one", "baseline"), Passed: true}, {Case: caseValue("fc_two", "fault"), Passed: false, Failure: "unsafe retry"}})
	directory := t.TempDir()
	if _, err := reporting.WriteFormats(directory, []string{"junit"}, report); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(directory, "infernosim-report.junit.xml"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Count(data, []byte("<testcase")) != 2 || bytes.Count(data, []byte("<failure")) != 1 {
		t.Fatalf("unexpected JUnit report:\n%s", data)
	}
}

func TestReliabilitySurfaceIsUnweightedAndCategoryScoped(t *testing.T) {
	results := []Result{
		{Case: agentreliability.Case{ID: "base", Description: "baseline"}, Passed: true},
		{Case: agentreliability.Case{ID: "one", FaultID: "lost", Category: "ambiguous_side_effect", Severity: "critical"}, Passed: true},
		{Case: agentreliability.Case{ID: "two", FaultID: "missing", Category: "tool_contract", Severity: "high"}, Passed: false},
	}
	surface := ReliabilitySurface(results)
	if !surface.BaselinePassed || surface.FaultCases != 2 || surface.FaultsPassed != 1 || surface.ObservedPassRate != 0.5 {
		t.Fatalf("surface = %#v", surface)
	}
	if len(surface.Categories) != 2 || surface.Categories[0].Category != "ambiguous_side_effect" {
		t.Fatalf("categories = %#v", surface.Categories)
	}
}

func caseValue(id, description string) agentreliability.Case {
	return agentreliability.Case{ID: id, Description: description}
}

func TestAgentRunnerHelper(t *testing.T) {
	if os.Getenv("GO_WANT_AGENT_HELPER") != "1" {
		return
	}
	proxyURL, err := url.Parse(os.Getenv("INFERNOSIM_PROXY_URL"))
	if err != nil {
		os.Exit(21)
	}
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL), DisableKeepAlives: true}}
	call := func(tool, id string) error {
		body := `{"jsonrpc":"2.0","id":"` + id + `","method":"tools/call","params":{"name":"` + tool + `","arguments":{"payment_id":"pay_1"}}}`
		request, requestErr := http.NewRequest(http.MethodPost, "http://mcp.test/mcp", bytes.NewBufferString(body))
		if requestErr != nil {
			return requestErr
		}
		request.Header.Set("Content-Type", "application/json")
		response, requestErr := client.Do(request)
		if requestErr != nil {
			return requestErr
		}
		defer response.Body.Close()
		_, _ = io.Copy(io.Discard, response.Body)
		if response.StatusCode >= 400 {
			return &statusError{status: response.StatusCode}
		}
		return nil
	}
	if err := call("payment.refund", "1"); err == nil {
		os.Exit(0)
	}
	if os.Getenv("AGENT_MODE") == "safe" {
		if err := call("payment.refund_status", "2"); err != nil {
			os.Exit(22)
		}
		os.Exit(0)
	}
	_ = call("payment.refund", "2")
	os.Exit(0)
}

type statusError struct{ status int }

func (e *statusError) Error() string { return http.StatusText(e.status) }

func writeAgentIncident(t *testing.T) string {
	t.Helper()
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "inbound.log"), []byte{}, 0o600); err != nil {
		t.Fatal(err)
	}
	responses := []event.Event{{
		ID: "refund", Type: "OutboundCall", Method: http.MethodPost, URL: "http://mcp.test/mcp", Status: 200,
		ResponseHeaders: http.Header{"Content-Type": {"application/json"}}, ResponseCaptured: true,
		ResponseBodyB64: base64.StdEncoding.EncodeToString([]byte(`{"jsonrpc":"2.0","id":"1","result":{"structuredContent":{"status":"refunded"}}}`)),
	}, {
		ID: "status", Type: "OutboundCall", Method: http.MethodPost, URL: "http://mcp.test/mcp", Status: 200,
		ResponseHeaders: http.Header{"Content-Type": {"application/json"}}, ResponseCaptured: true,
		ResponseBodyB64: base64.StdEncoding.EncodeToString([]byte(`{"jsonrpc":"2.0","id":"2","result":{"structuredContent":{"status":"refunded"}}}`)),
	}}
	var log bytes.Buffer
	encoder := json.NewEncoder(&log)
	for _, response := range responses {
		if err := encoder.Encode(response); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(directory, "outbound.log"), log.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	config := `agent:
  version: 1
  enabled: true
  adapters:
    mcp: true
  limits:
    max_cases: 20
    max_calls: 8
    max_body_bytes: 262144
  effects:
    - name: payment.refund
      select:
        tool: payment.refund
      identity:
        - $.arguments.payment_id
  faults:
    - id: refund-response-lost
      description: refund commits but its response is lost
      select:
        kind: mcp_tool_result
        tool: payment.refund
        occurrence: 1
      committed_response_lost: true
  assertions:
    - id: refund-exactly-once
      type: exactly_once_effect
      effect: payment.refund
    - id: verify-before-retry
      type: require_verification_before_retry
      effect: payment.refund
      verification_tool: payment.refund_status
    - id: recorded-universe-only
      type: no_unexpected_calls
`
	if err := os.WriteFile(filepath.Join(directory, "replay.yaml"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	return directory
}
