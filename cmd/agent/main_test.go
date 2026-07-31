package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"infernosim/pkg/event"
	"infernosim/pkg/replaydriver"
)

func TestSummaryProducedOnInvalidConfig(t *testing.T) {
	summary := NewReplaySummary()
	summary.Outcome = "FAIL_INVALID_ENV"
	summary.PrimaryFailureReason = "invalid inject rule"
	summary.RunsRequested = 1
	summary.Finalize()
	joined := strings.Join(summary.Lines, "\n")
	if !strings.Contains(joined, "InfernoSIM Replay Summary") {
		t.Fatal("summary missing header")
	}
}

func TestSummaryProducedOnTimeExpanded(t *testing.T) {
	summary := NewReplaySummary()
	summary.Outcome = "FAIL_STALLED"
	summary.PrimaryFailureReason = "replay stalled"
	summary.RunsRequested = 1
	summary.Finalize()
	joined := strings.Join(summary.Lines, "\n")
	if !strings.Contains(joined, "InfernoSIM Replay Summary") {
		t.Fatal("summary missing header")
	}
}

func TestSummaryProducedOnNonDeterminism(t *testing.T) {
	summary := NewReplaySummary()
	summary.RunsRequested = 2
	summary.RunsExecuted = 2
	summary.RunsCompleted = 2
	summary.InboundEventsReplayed = 2
	summary.OutboundEventsObserved = 2
	summary.ProxyStatus = "BOUND"
	summary.DependenciesExercised = true
	summary.NonDeterministicRuns = 1
	summary.Finalize()
	if summary.Outcome != "FAIL_NON_DETERMINISTIC" {
		t.Fatalf("expected FAIL_NON_DETERMINISTIC, got %s", summary.Outcome)
	}
	joined := strings.Join(summary.Lines, "\n")
	if !strings.Contains(joined, "InfernoSIM Replay Summary") {
		t.Fatal("summary missing header")
	}
}

func TestOutcomeNoCoverageWhenNotTransparent(t *testing.T) {
	summary := NewReplaySummary()
	summary.ProxyStatus = "BOUND"
	summary.OutboundEventsExpected = 1
	summary.OutboundEventsObserved = 0
	summary.TransparentMode = false
	summary.RunsRequested = 1
	summary.RunsExecuted = 1
	summary.RunsCompleted = 1
	summary.InboundEventsReplayed = 1
	summary.Finalize()
	if summary.Outcome != "FAIL_NO_COVERAGE" {
		t.Fatalf("expected FAIL_NO_COVERAGE, got %s", summary.Outcome)
	}
}

func TestOutcomeInboundOnlyIsWeakPass(t *testing.T) {
	summary := NewReplaySummary()
	summary.ProxyStatus = "BOUND"
	summary.RunsRequested = 1
	summary.RunsExecuted = 1
	summary.RunsCompleted = 1
	summary.InboundEventsReplayed = 1
	summary.TargetInbound = 1
	summary.Finalize()
	if summary.Outcome != "PASS_WEAK" {
		t.Fatalf("expected PASS_WEAK, got %s", summary.Outcome)
	}
}

func TestOutcomeTransparentProxyOnlyWhenTransparent(t *testing.T) {
	summary := NewReplaySummary()
	summary.ProxyStatus = "BOUND"
	summary.OutboundEventsExpected = 1
	summary.OutboundEventsObserved = 0
	summary.TransparentMode = true
	summary.RunsRequested = 1
	summary.RunsExecuted = 1
	summary.RunsCompleted = 1
	summary.InboundEventsReplayed = 1
	summary.Finalize()
	if summary.Outcome != "FAIL_TRANSPARENT_PROXY" {
		t.Fatalf("expected FAIL_TRANSPARENT_PROXY, got %s", summary.Outcome)
	}
}

func TestOutcomeSLOMissed(t *testing.T) {
	summary := NewReplaySummary()
	summary.ProxyStatus = "BOUND"
	summary.OutboundEventsObserved = 10
	summary.DependenciesExercised = true
	summary.Window = 5 * time.Second
	summary.TargetInbound = 100
	summary.InboundEventsReplayed = 10
	summary.RunsRequested = 1
	summary.RunsExecuted = 1
	summary.RunsCompleted = 1
	summary.Finalize()
	if summary.Outcome != "FAIL_SLO_MISSED" {
		t.Fatalf("expected FAIL_SLO_MISSED, got %s", summary.Outcome)
	}
}

func TestGenerateLintAndExplainCLI(t *testing.T) {
	output := filepath.Join(t.TempDir(), "replay.yaml")
	protoPath, err := filepath.Abs(filepath.Join("..", "..", "examples", "grpcapp", "echo", "echo.proto"))
	if err != nil {
		t.Fatal(err)
	}
	if code := runGenerate([]string{
		"--proto", protoPath,
		"--import-path", filepath.Dir(protoPath),
		"--out", output,
	}); code != 0 {
		t.Fatalf("generate code=%d", code)
	}
	if code := runLint([]string{output}); code != 0 {
		t.Fatalf("lint code=%d", code)
	}
	if _, err := replaydriver.LoadReplayConfig(filepath.Join("..", "..", "examples", "replay-v3.yaml")); err != nil {
		t.Fatalf("v3 example: %v", err)
	}

	incident := filepath.Join(t.TempDir(), "incident")
	if err := os.MkdirAll(incident, 0o700); err != nil {
		t.Fatal(err)
	}
	outbound, err := os.OpenFile(filepath.Join(incident, "outbound.log"), os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	frame := "AAAAAAcKBWhlbGxv"
	if err := json.NewEncoder(outbound).Encode(event.Event{
		Type:    "OutboundCall",
		Method:  http.MethodPost,
		URL:     "https://dependency.test/echo.EchoService/Echo",
		BodyB64: frame,
	}); err != nil {
		t.Fatal(err)
	}
	if err := outbound.Close(); err != nil {
		t.Fatal(err)
	}
	requestPath := filepath.Join(t.TempDir(), "request.json")
	request := `{"method":"POST","url":"https://dependency.test/echo.EchoService/Echo","headers":{"Content-Type":["application/grpc"]},"body_base64":"` + frame + `"}`
	if err := os.WriteFile(requestPath, []byte(request), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := runMatch([]string{"explain", incident, "--request", requestPath, "--config", output}); code != 0 {
		t.Fatalf("match explain code=%d", code)
	}
}
