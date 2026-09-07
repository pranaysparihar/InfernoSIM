package agentrunner

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"infernosim/pkg/agentreliability"
)

func TestCrashRestartPreservesCheckpointAndGroundTruth(t *testing.T) {
	for _, mode := range []string{"restart-safe", "restart-unsafe"} {
		t.Run(mode, func(t *testing.T) {
			r, err := Run(context.Background(), Options{IncidentDir: writeAgentIncident(t), Command: []string{os.Args[0], "-test.run=TestAgentRunnerHelper", "--"}, Environment: []string{"GO_WANT_AGENT_HELPER=1", "AGENT_MODE=" + mode}, RestartAfterCall: 1, Timeout: 10 * time.Second})
			if err != nil {
				t.Fatal(err)
			}
			if !r.Process.Restarted || r.Process.ExitCode != 0 || r.Passed != (mode == "restart-safe") {
				t.Fatalf("%+v failure=%s", r.Process, r.Failure)
			}
			if r.ScopeHash == "" {
				t.Fatal("missing comparison scope")
			}
		})
	}
}

func TestUnreachedCrashBoundaryFails(t *testing.T) {
	r, err := Run(context.Background(), Options{IncidentDir: writeAgentIncident(t), Command: []string{os.Args[0], "-test.run=TestAgentRunnerHelper", "--"}, Environment: []string{"GO_WANT_AGENT_HELPER=1", "AGENT_MODE=safe"}, RestartAfterCall: 8})
	if err != nil {
		t.Fatal(err)
	}
	if r.Passed || !strings.Contains(r.Failure, "not exercised") {
		t.Fatal(r.Failure)
	}
}

func TestCompareRejectsCoverageLossAndScopeChanges(t *testing.T) {
	b := Result{ScopeHash: "scope", Case: agentreliability.Case{ID: "one"}, Passed: true, Assertions: []agentreliability.AssertionResult{{ID: "guard", Type: "approval_before_effect", Passed: true, Coverage: "exercised"}}}
	c := b
	c.Assertions = []agentreliability.AssertionResult{{ID: "guard", Type: "approval_before_effect", Passed: true, Coverage: "not_exercised"}}
	r, err := CompareResults([]Result{b}, []Result{c})
	if err != nil || r.Passed || len(r.Regressions) != 1 {
		t.Fatalf("%+v %v", r, err)
	}
	c.ScopeHash = "different"
	if _, err := CompareResults([]Result{b}, []Result{c}); err == nil {
		t.Fatal("compared different evidence")
	}
	c = b
	c.Assertions = nil
	r, err = CompareResults([]Result{b}, []Result{c})
	if err != nil || r.Passed {
		t.Fatal("disappearing assertion passed")
	}
	if _, err := CompareResults([]Result{b}, []Result{b, b}); err == nil {
		t.Fatal("case sets not checked")
	}
}

func TestReductionPreservesNamedFailure(t *testing.T) {
	original := agentreliability.Case{ID: "original", FaultIDs: []string{"a", "b"}}
	run := func(ctx context.Context, c agentreliability.Case) (Result, error) {
		failed := contains(c.ActiveFaults(), "a")
		return Result{Passed: !failed, Assertions: []agentreliability.AssertionResult{{ID: "target", Passed: !failed, Coverage: map[bool]string{true: "violated", false: "exercised"}[failed]}}}, nil
	}
	r, err := ReduceFaults(context.Background(), original, "target", 20, run)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Complete || len(r.Case.ActiveFaults()) != 1 || r.Case.ActiveFaults()[0] != "a" {
		t.Fatalf("%+v", r)
	}
	limited, err := ReduceFaults(context.Background(), original, "target", 2, run)
	if err != nil || limited.Complete || limited.Attempts != 2 {
		t.Fatalf("%+v %v", limited, err)
	}
	if _, err := ReduceFaults(context.Background(), original, "other", 20, run); err == nil {
		t.Fatal("different failure accepted")
	}
}

func TestCombinedCasesAreNotBaselines(t *testing.T) {
	s := ReliabilitySurface([]Result{{Case: agentreliability.Case{ID: "base"}, Passed: true}, {Case: agentreliability.Case{ID: "pair", FaultIDs: []string{"a", "b"}}, Passed: false}})
	if !s.BaselinePassed || s.FaultCases != 1 || s.FaultsFailed != 1 {
		t.Fatal(s)
	}
}

func TestReducerRejectsInvalidReplayEvenWhenAssertionFails(t *testing.T) {
	run := func(context.Context, agentreliability.Case) (Result, error) {
		return Result{InvalidRun: true, Assertions: []agentreliability.AssertionResult{{ID: "target", Passed: false, Coverage: "violated"}}}, nil
	}
	if _, err := ReduceFaults(context.Background(), agentreliability.Case{ID: "c", FaultID: "f"}, "target", 4, run); err == nil {
		t.Fatal("invalid replay accepted as a reliable failure")
	}
}
