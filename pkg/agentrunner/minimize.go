package agentrunner

import (
	"context"
	"fmt"
	"infernosim/pkg/agentreliability"
	"strings"
)

type Minimization struct {
	Version   int    `json:"version"`
	Trial     Trial  `json:"trial"`
	Assertion string `json:"assertion"`
	Attempts  int    `json:"attempts"`
	Complete  bool   `json:"complete"`
}

func copyTrial(t Trial) Trial {
	c := t
	c.Case.FaultIDs = append([]string(nil), t.Case.ActiveFaults()...)
	c.Case.FaultID = ""
	c.Occurrences = map[string]int{}
	for k, v := range t.Occurrences {
		c.Occurrences[k] = v
	}
	if t.Schedule != nil {
		s := *t.Schedule
		s.Steps = append([]agentreliability.Selector(nil), s.Steps...)
		c.Schedule = &s
	}
	return c
}
func shrinkTrials(t Trial) []Trial {
	var out []Trial
	for i := range t.Case.ActiveFaults() {
		c := copyTrial(t)
		id := c.Case.FaultIDs[i]
		c.Case.FaultIDs = append(c.Case.FaultIDs[:i], c.Case.FaultIDs[i+1:]...)
		delete(c.Occurrences, id)
		out = append(out, c)
	}
	if t.Schedule != nil {
		c := copyTrial(t)
		c.Schedule = nil
		c.Case.ScheduleID = ""
		out = append(out, c)
		if len(t.Schedule.Steps) > 2 {
			for i := range t.Schedule.Steps {
				c := copyTrial(t)
				c.Schedule.Partial = true
				c.Schedule.Steps = append(c.Schedule.Steps[:i], c.Schedule.Steps[i+1:]...)
				out = append(out, c)
			}
		}
	}
	for _, id := range t.Case.ActiveFaults() {
		if n := t.Occurrences[id]; n > 1 {
			for _, smaller := range []int{1, n / 2} {
				if smaller >= 1 && smaller < n {
					c := copyTrial(t)
					c.Occurrences[id] = smaller
					out = append(out, c)
				}
			}
		}
	}
	return out
}

// MinimizeTrial repeatedly removes fault membership/admission constraints and
// lowers occurrence positions. Complete is a fixed point under these moves,
// not global trace minimality. Every accepted candidate must reproduce twice.
func MinimizeTrial(ctx context.Context, t Trial, assertion string, budget int, run func(context.Context, Trial) (Result, error)) (Minimization, error) {
	r := Minimization{Version: 1, Trial: t, Assertion: assertion}
	if assertion == "" || strings.HasPrefix(assertion, "schedule:") || budget < 2 || budget > 1000 {
		return r, fmt.Errorf("minimization requires an application assertion and budget 2..1000")
	}
	persists := func(c Trial) (bool, error) {
		for i := 0; i < 2; i++ {
			if err := ctx.Err(); err != nil {
				return false, err
			}
			r.Attempts++
			result, err := run(ctx, c)
			if err != nil {
				return false, err
			}
			if result.InvalidRun || result.Process.TimedOut || result.Process.ExitCode != 0 {
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
	ok, err := persists(t)
	if err != nil {
		return r, err
	}
	if !ok {
		return r, fmt.Errorf("named assertion did not reproduce twice in valid executions")
	}
	for {
		changed := false
		for _, c := range shrinkTrials(r.Trial) {
			if r.Attempts+2 > budget {
				return r, nil
			}
			c = identifyTrial(c, "minimized", 0)
			ok, err := persists(c)
			if err != nil {
				return r, err
			}
			if ok {
				r.Trial = c
				changed = true
				break
			}
		}
		if !changed {
			r.Complete = true
			return r, nil
		}
	}
}
