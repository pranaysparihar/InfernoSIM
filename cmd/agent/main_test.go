package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeTestLogs(t *testing.T, dir string) (string, string) {
	t.Helper()
	inbound := filepath.Join(dir, "inbound.log")
	outbound := filepath.Join(dir, "outbound.log")

	inboundLine := `{"id":"1","type":"InboundRequest","timestamp":"2026-02-02T08:30:43Z","method":"GET","url":"http://localhost:18081/api/test?q=verify"}`
	outboundLine := `{"id":"2","type":"OutboundCall","timestamp":"2026-02-02T08:30:43Z","method":"GET","url":"http://worldtimeapi.org/api/timezone/Etc/UTC","status":200}`

	if err := os.WriteFile(inbound, []byte(inboundLine+"\n"), 0644); err != nil {
		t.Fatalf("write inbound: %v", err)
	}
	if err := os.WriteFile(outbound, []byte(outboundLine+"\n"), 0644); err != nil {
		t.Fatalf("write outbound: %v", err)
	}
	return inbound, outbound
}

func TestSummaryProducedOnInvalidConfig(t *testing.T) {
	tmp := t.TempDir()
	inbound, outbound := writeTestLogs(t, tmp)

	summary := executeReplay(replayExecutionInput{
		Runs:        1,
		TimeScale:   1.0,
		Density:     1.0,
		MinGap:      0,
		MaxWallTime: 2 * time.Second,
		MaxIdleTime: 1 * time.Second,
		InboundLog:  inbound,
		OutboundLog: outbound,
		InjectFlags: []string{"dep=redis error=10%"},
		TargetBase:  "http://localhost:18080",
		SkipStub:    true,
	})

	if summary.Outcome != DivergenceInvalidConfig {
		t.Fatalf("expected INVALID_CONFIG, got %s", summary.Outcome)
	}
	joined := strings.Join(summary.Lines, "\n")
	if !strings.Contains(joined, "InfernoSIM Replay Summary") {
		t.Fatal("summary missing header")
	}
}

func TestSummaryProducedOnTimeExpanded(t *testing.T) {
	tmp := t.TempDir()
	inbound, outbound := writeTestLogs(t, tmp)

	summary := executeReplay(replayExecutionInput{
		Runs:        1,
		TimeScale:   1.0,
		Density:     1.0,
		MinGap:      500 * time.Millisecond,
		MaxWallTime: 1 * time.Millisecond,
		MaxIdleTime: 1 * time.Second,
		InboundLog:  inbound,
		OutboundLog: outbound,
		TargetBase:  "http://localhost:18080",
		SkipStub:    true,
	})

	if summary.Outcome != DivergenceTimeExpanded {
		t.Fatalf("expected TIME_EXPANDED, got %s", summary.Outcome)
	}
	joined := strings.Join(summary.Lines, "\n")
	if !strings.Contains(joined, "InfernoSIM Replay Summary") {
		t.Fatal("summary missing header")
	}
}

func TestSummaryProducedOnNonDeterminism(t *testing.T) {
	summary := buildSummary(replaySummaryInput{
		RunsAttempted: 2,
		Outcomes: []ReplayOutcome{
			{
				RunIndex:       1,
				Completed:      true,
				DivergenceType: DivergenceDeterministic,
			},
			{
				RunIndex:       2,
				Completed:      true,
				DivergenceType: DivergenceNonDeterministic,
				Detail:         "dependency variance",
			},
		},
	})

	if summary.Outcome != DivergenceNonDeterministic {
		t.Fatalf("expected NON_DETERMINISTIC, got %s", summary.Outcome)
	}
	joined := strings.Join(summary.Lines, "\n")
	if !strings.Contains(joined, "InfernoSIM Replay Summary") {
		t.Fatal("summary missing header")
	}
}
