package agentrunner

import (
	"context"
	"fmt"
	"sort"

	"infernosim/pkg/agentreliability"
)

type Comparison struct {
	Version           int      `json:"version"`
	Passed            bool     `json:"passed"`
	Regressions       []string `json:"regressions"`
	Improvements      []string `json:"improvements"`
	CandidateFailures []string `json:"candidate_failures"`
}

// CompareResults rejects incomparable evidence instead of treating missing
// cases or disappearing assertions as an improvement. Runtime duration is not
// a correctness signal and live model quality is not graded here.
func CompareResults(baseline, candidate []Result) (Comparison, error) {
	r := Comparison{Version: 1, Passed: true}
	if len(baseline) == 0 || len(baseline) != len(candidate) {
		return r, fmt.Errorf("comparison requires equal nonempty case sets")
	}
	old := map[string]Result{}
	for _, b := range baseline {
		if b.Case.ID == "" || old[b.Case.ID].Case.ID != "" {
			return r, fmt.Errorf("duplicate or empty baseline case ID")
		}
		old[b.Case.ID] = b
	}
	seen := map[string]bool{}
	for _, c := range candidate {
		b, ok := old[c.Case.ID]
		if !ok || seen[c.Case.ID] {
			return r, fmt.Errorf("different or duplicate candidate case set")
		}
		seen[c.Case.ID] = true
		if b.ScopeHash == "" || b.ScopeHash != c.ScopeHash || b.RestartAfterCall != c.RestartAfterCall || agentreliability.StableHash(b.Case) != agentreliability.StableHash(c.Case) {
			return r, fmt.Errorf("case %s has different evidence or execution settings", c.Case.ID)
		}
		if !c.Passed {
			r.CandidateFailures = append(r.CandidateFailures, c.Case.ID)
		}
		if b.Passed && !c.Passed {
			r.Regressions = append(r.Regressions, c.Case.ID+": case failed")
		}
		if !b.Passed && c.Passed {
			r.Improvements = append(r.Improvements, c.Case.ID+": case passed")
		}
		before := map[string]agentreliability.AssertionResult{}
		for _, a := range b.Assertions {
			if _, exists := before[a.ID]; exists {
				return r, fmt.Errorf("duplicate baseline assertion")
			}
			before[a.ID] = a
		}
		after := map[string]bool{}
		for _, a := range c.Assertions {
			if after[a.ID] {
				return r, fmt.Errorf("duplicate candidate assertion")
			}
			after[a.ID] = true
			if prior, ok := before[a.ID]; ok {
				if prior.Type != a.Type {
					return r, fmt.Errorf("changed assertion type")
				}
				if prior.Passed && !a.Passed {
					r.Regressions = append(r.Regressions, c.Case.ID+":"+a.ID+": assertion failed")
				}
				if prior.Coverage != "not_exercised" && a.Coverage == "not_exercised" {
					r.Regressions = append(r.Regressions, c.Case.ID+":"+a.ID+": safety coverage lost")
				}
			}
		}
		for id := range before {
			if !after[id] {
				r.Regressions = append(r.Regressions, c.Case.ID+":"+id+": assertion disappeared")
			}
		}
	}
	sort.Strings(r.Regressions)
	sort.Strings(r.Improvements)
	sort.Strings(r.CandidateFailures)
	r.Passed = len(r.Regressions) == 0 && len(r.CandidateFailures) == 0
	return r, nil
}

type Reduction struct {
	Version   int                   `json:"version"`
	Case      agentreliability.Case `json:"case"`
	Assertion string                `json:"assertion"`
	Attempts  int                   `json:"attempts"`
	Complete  bool                  `json:"complete"`
}

// ReduceFaults preserves one named violated assertion on two consecutive
// executions of every accepted candidate. It reduces only fault membership;
// it never edits traffic, weakens assertions, or diagnoses process crashes as
// the target failure. Complete means 1-minimal, not a globally minimal trace.
func ReduceFaults(ctx context.Context, original agentreliability.Case, assertion string, budget int, run func(context.Context, agentreliability.Case) (Result, error)) (Reduction, error) {
	r := Reduction{Version: 1, Case: original, Assertion: assertion}
	if assertion == "" || budget < 2 || budget > 100 {
		return r, fmt.Errorf("reduction requires assertion and budget between 2 and 100")
	}
	persists := func(c agentreliability.Case) (bool, error) {
		if r.Attempts+2 > budget {
			return false, nil
		}
		for repeat := 0; repeat < 2; repeat++ {
			if err := ctx.Err(); err != nil {
				return false, err
			}
			r.Attempts++
			result, err := run(ctx, c)
			if err != nil {
				return false, err
			}
			if result.Process.ExitCode != 0 || result.Process.TimedOut || result.InvalidRun {
				return false, nil
			}
			found := false
			for _, a := range result.Assertions {
				if a.ID == assertion && !a.Passed && a.Coverage == "violated" {
					found = true
				}
			}
			if !found {
				return false, nil
			}
		}
		return true, nil
	}
	ok, err := persists(original)
	if err != nil {
		return r, err
	}
	if !ok {
		return r, fmt.Errorf("target assertion did not reproduce twice without a process failure")
	}
	ids := original.ActiveFaults()
	for i := 0; i < len(ids); {
		if r.Attempts+2 > budget {
			return r, nil
		}
		next := append(append([]string{}, ids[:i]...), ids[i+1:]...)
		c := original
		c.FaultID = ""
		c.FaultIDs = next
		c.ID = "reduced_" + agentreliability.StableHash(struct {
			Original string
			Faults   []string
		}{original.ID, next})[:16]
		c.Description = "reduced fault set for " + assertion
		ok, err := persists(c)
		if err != nil {
			return r, err
		}
		if ok {
			ids = next
			r.Case = c
			i = 0
		} else {
			i++
		}
	}
	r.Complete = true
	return r, nil
}
