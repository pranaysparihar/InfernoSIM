package main

import (
	"strings"
	"testing"
	"time"
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
