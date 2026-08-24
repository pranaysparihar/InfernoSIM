// Command agentbenchmark repeatedly exercises the checked-in safe and unsafe
// agent controls against the deterministic v4 fault matrix. Its JSON output is
// raw evidence rather than a composite marketing score.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"infernosim/pkg/agentreliability"
	"infernosim/pkg/agentrunner"
	"infernosim/pkg/reporting"
)

type caseResult struct {
	ID              string  `json:"id"`
	FaultID         string  `json:"fault_id,omitempty"`
	Description     string  `json:"description"`
	Category        string  `json:"category,omitempty"`
	Severity        string  `json:"severity,omitempty"`
	SafePassed      int     `json:"safe_passed"`
	SafeFailed      int     `json:"safe_failed"`
	UnsafePassed    int     `json:"unsafe_passed"`
	UnsafeRejected  int     `json:"unsafe_rejected"`
	SafeP50Millis   float64 `json:"safe_p50_ms"`
	SafeP95Millis   float64 `json:"safe_p95_ms"`
	UnsafeP50Millis float64 `json:"unsafe_p50_ms"`
	UnsafeP95Millis float64 `json:"unsafe_p95_ms"`
	safeDurations   []float64
	unsafeDurations []float64
}

type report struct {
	Schema             string       `json:"schema"`
	Generated          time.Time    `json:"generated"`
	Fixture            string       `json:"fixture"`
	ScopeHash          string       `json:"scope_hash"`
	CasePlanHash       string       `json:"case_plan_hash"`
	Iterations         int          `json:"iterations"`
	CasesPerIteration  int          `json:"cases_per_iteration"`
	SafeExecutions     int          `json:"safe_executions"`
	SafePassed         int          `json:"safe_passed"`
	UnsafeControls     int          `json:"unsafe_controls"`
	UnsafeRejected     int          `json:"unsafe_rejected"`
	BaselinePassed     int          `json:"unsafe_baseline_passed"`
	DeterministicPlan  bool         `json:"deterministic_plan"`
	BehaviorPassed     bool         `json:"behavior_passed"`
	RawContentRetained bool         `json:"raw_content_retained"`
	Cases              []caseResult `json:"cases"`
}

func main() {
	fixture := flag.String("fixture", "examples/agent-reliability", "Sanitized agent incident fixture")
	agentCommand := flag.String("agent-command", "", "Path to the agentlab executable")
	runs := flag.Int("runs", 20, "Number of complete safe/unsafe iterations")
	out := flag.String("out", "benchmarks/results/agent-reliability.json", "Raw JSON result path")
	timeout := flag.Duration("timeout", 30*time.Second, "Per-case command timeout")
	flag.Parse()
	if *agentCommand == "" {
		fatal("--agent-command is required")
	}
	if *runs < 1 || *runs > 1000 {
		fatal("runs must be between 1 and 1000")
	}
	prepared, err := agentrunner.Prepare(*fixture, "", 0, agentreliability.MaximumCases, true)
	if err != nil {
		fatal(err.Error())
	}
	planHash := agentreliability.StableHash(prepared.Cases)
	result := report{
		Schema: "infernosim.agent-reliability-benchmark.v1", Generated: time.Now().UTC(),
		Fixture: filepath.ToSlash(filepath.Clean(*fixture)), ScopeHash: prepared.ScopeHash,
		CasePlanHash: planHash, Iterations: *runs, CasesPerIteration: len(prepared.Cases),
		DeterministicPlan: true, BehaviorPassed: true, RawContentRetained: false,
	}
	caseIndexes := make(map[string]int)
	for _, plannedCase := range prepared.Cases {
		caseIndexes[plannedCase.ID] = len(result.Cases)
		result.Cases = append(result.Cases, caseResult{
			ID: plannedCase.ID, FaultID: plannedCase.FaultID, Description: plannedCase.Description,
			Category: plannedCase.Category, Severity: plannedCase.Severity,
		})
	}
	for iteration := 0; iteration < *runs; iteration++ {
		again, prepareErr := agentrunner.Prepare(*fixture, "", 0, agentreliability.MaximumCases, true)
		if prepareErr != nil {
			fatal(prepareErr.Error())
		}
		if agentreliability.StableHash(again.Cases) != planHash || again.ScopeHash != prepared.ScopeHash {
			result.DeterministicPlan = false
			result.BehaviorPassed = false
		}
		for _, plannedCase := range again.Cases {
			index := caseIndexes[plannedCase.ID]
			safe, runErr := agentrunner.Run(context.Background(), agentrunner.Options{
				IncidentDir: *fixture, Case: plannedCase, Command: []string{*agentCommand, "--mode=safe"}, Timeout: *timeout,
			})
			if runErr != nil {
				fatal(fmt.Sprintf("safe case %s: %v", plannedCase.ID, runErr))
			}
			result.SafeExecutions++
			result.Cases[index].safeDurations = append(result.Cases[index].safeDurations, milliseconds(safe.Duration))
			if safe.Passed {
				result.SafePassed++
				result.Cases[index].SafePassed++
			} else {
				result.Cases[index].SafeFailed++
				result.BehaviorPassed = false
			}

			unsafe, runErr := agentrunner.Run(context.Background(), agentrunner.Options{
				IncidentDir: *fixture, Case: plannedCase, Command: []string{*agentCommand, "--mode=unsafe"}, Timeout: *timeout,
			})
			if runErr != nil {
				fatal(fmt.Sprintf("unsafe case %s: %v", plannedCase.ID, runErr))
			}
			result.Cases[index].unsafeDurations = append(result.Cases[index].unsafeDurations, milliseconds(unsafe.Duration))
			if plannedCase.FaultID == "" {
				if unsafe.Passed {
					result.BaselinePassed++
					result.Cases[index].UnsafePassed++
				} else {
					result.Cases[index].UnsafeRejected++
					result.BehaviorPassed = false
				}
				continue
			}
			result.UnsafeControls++
			if unsafe.Passed {
				result.Cases[index].UnsafePassed++
				result.BehaviorPassed = false
			} else {
				result.UnsafeRejected++
				result.Cases[index].UnsafeRejected++
			}
		}
	}
	for index := range result.Cases {
		item := &result.Cases[index]
		item.SafeP50Millis = percentile(item.safeDurations, 0.50)
		item.SafeP95Millis = percentile(item.safeDurations, 0.95)
		item.UnsafeP50Millis = percentile(item.unsafeDurations, 0.50)
		item.UnsafeP95Millis = percentile(item.unsafeDurations, 0.95)
		item.safeDurations = nil
		item.unsafeDurations = nil
	}
	if result.SafePassed != result.SafeExecutions || result.UnsafeRejected != result.UnsafeControls || result.BaselinePassed != *runs {
		result.BehaviorPassed = false
	}
	encoded, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		fatal(err.Error())
	}
	if err := reporting.WritePrivateFile(*out, append(encoded, '\n')); err != nil {
		fatal(err.Error())
	}
	fmt.Printf("agent benchmark: safe=%d/%d unsafe-rejected=%d/%d deterministic=%t output=%s\n",
		result.SafePassed, result.SafeExecutions, result.UnsafeRejected, result.UnsafeControls, result.DeterministicPlan, *out)
	if !result.BehaviorPassed {
		os.Exit(1)
	}
}

func milliseconds(value time.Duration) float64 {
	return float64(value.Microseconds()) / 1000
}

func percentile(values []float64, fraction float64) float64 {
	if len(values) == 0 {
		return 0
	}
	ordered := append([]float64(nil), values...)
	sort.Float64s(ordered)
	position := int(float64(len(ordered)-1)*fraction + 0.5)
	return ordered[position]
}

func fatal(message string) {
	fmt.Fprintln(os.Stderr, "agent benchmark:", message)
	os.Exit(1)
}
