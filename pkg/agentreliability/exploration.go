package agentreliability

import (
	"context"
	"fmt"
	"strings"
	"time"

	"infernosim/pkg/jsonpath"
)

// Exploration is deliberately bounded. Pairwise means enumerated pairs, not
// a claim that all pairs fit inside the caller's case budget.
type Exploration struct {
	Pairwise bool `yaml:"pairwise" json:"pairwise"`
}

// Schedule controls admission at the simulated protocol boundary. It does not
// control application threads, response delivery, or unobserved operations.
type Schedule struct {
	ID      string     `yaml:"id" json:"id"`
	Steps   []Selector `yaml:"steps" json:"steps"`
	Timeout string     `yaml:"timeout" json:"timeout"`
	Partial bool       `yaml:"partial,omitempty" json:"partial,omitempty"`
}

func (c Config) validateReliability() error {
	if len(c.Faults) > MaximumCases || len(c.Schedules) > 100 {
		return fmt.Errorf("too many faults or schedules")
	}
	seen := map[string]bool{}
	for _, s := range c.Schedules {
		if s.ID == "" || seen[s.ID] || len(s.Steps) == 0 || len(s.Steps) > MaximumCalls {
			return fmt.Errorf("schedule requires unique id and 1-%d steps", MaximumCalls)
		}
		seen[s.ID] = true
		callIDs := map[string]bool{}
		if d, err := parseBoundedDuration("schedule.timeout", s.Timeout, false); err != nil || d == 0 {
			return fmt.Errorf("schedule %s requires timeout > 0 and <= 60s", s.ID)
		}
		for _, step := range s.Steps {
			if callIDs[step.CallID] {
				return fmt.Errorf("schedule %s repeats call_id %q", s.ID, step.CallID)
			}
			callIDs[step.CallID] = true
			if err := validateSelector("schedule step", step); err != nil {
				return err
			}
			if step.Occurrence != 0 || step.CallID == "" {
				return fmt.Errorf("schedule steps require call_id and cannot use occurrence")
			}
		}
	}
	return nil
}

func validateSafetyAssertion(a Assertion, effects map[string]struct{}) error {
	for _, p := range append(append([]string{}, a.Bind...), a.Path, a.VerificationPath, a.VersionPath, a.EstimatePath) {
		if p != "" {
			if err := jsonpath.Validate(p); err != nil {
				return err
			}
		}
	}
	if a.Max < 0 || a.MaxAgeCalls < 0 {
		return fmt.Errorf("max and max_age_calls must be nonnegative")
	}
	switch a.Type {
	case "approval_before_effect", "compensated_effect", "monitor_sound":
		if _, ok := effects[a.Effect]; !ok {
			return fmt.Errorf("effect must be declared")
		}
	}
	switch a.Type {
	case "max_cost":
		if a.PricePerTokenMicrounits <= 0 || a.PricePerTokenMicrounits > 1_000_000_000 {
			return fmt.Errorf("max_cost requires price_per_token_microunits between 1 and 1000000000")
		}
	case "approval_before_effect":
		if a.VerificationTool == "" || a.VerificationPath == "" || a.VerificationValue == nil || len(a.Bind) == 0 || a.MaxAgeCalls < 1 {
			return fmt.Errorf("approval requires verification_tool, verification_path/value, bind and max_age_calls > 0")
		}
	case "compensated_effect":
		if _, ok := effects[a.Compensation]; !ok || a.Compensation == a.Effect {
			return fmt.Errorf("compensation must reference a different declared effect")
		}
	case "monitor_sound":
		if a.Tool == "" || a.Path == "" || a.Expected == nil {
			return fmt.Errorf("monitor requires tool, path and expected healthy value")
		}
	case "request_matches":
		if a.Tool == "" || a.Path == "" || a.Expected == nil {
			return fmt.Errorf("request_matches requires tool, path and expected")
		}
	case "no_calls_after_cancel":
		if a.Tool == "" {
			return fmt.Errorf("no_calls_after_cancel requires target tool")
		}
	}
	return nil
}

func appendExploration(cases []Case, c Config, scope string, seed int64, budget int, singles []Case) []Case {
	add := func(ids []string, schedule string) {
		ids = UniqueFaults(ids)
		key := StableHash(struct {
			Faults   []string
			Schedule string
		}{ids, schedule})
		cases = append(cases, Case{ID: caseID(scope, seed, key), FaultIDs: ids, ScheduleID: schedule,
			Description: "faults [" + strings.Join(ids, ", ") + "] schedule [" + schedule + "]", Category: "combined", Severity: "high"})
	}
	// Schedule-only cases precede combinations so a small budget can exercise
	// an ordering without conflating it with injected corruption.
	for _, s := range c.Schedules {
		if len(cases) >= budget {
			return cases
		}
		add(nil, s.ID)
	}
	if c.Exploration.Pairwise {
		for i, a := range singles {
			for _, b := range singles[i+1:] {
				if len(cases) >= budget {
					return cases
				}
				add([]string{a.FaultID, b.FaultID}, "")
			}
		}
	}
	for _, s := range c.Schedules {
		for _, f := range singles {
			if len(cases) >= budget {
				return cases
			}
			add([]string{f.FaultID}, s.ID)
		}
	}
	return cases
}

func (e *Engine) SetSchedule(id string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.sequence != 0 {
		return fmt.Errorf("cannot change schedule after execution starts")
	}
	for _, s := range e.config.Schedules {
		if s.ID == id {
			e.schedule = &s
			e.scheduleChanged = make(chan struct{})
			return nil
		}
	}
	return fmt.Errorf("unknown schedule %q", id)
}

// SetBeforeResponse installs a runner-owned boundary hook before execution.
// Returning true marks this response lost, including any simulated effects.
func (e *Engine) SetBeforeResponse(hook func(int) bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.beforeResponse = hook
}

// ProcessContext waits for matching requests out of order, then atomically
// admits each step and processes it. Unscheduled calls fail closed. A missing
// participant times out rather than silently weakening the requested schedule.
func (e *Engine) ProcessContext(ctx context.Context, req Request, resp Response) (Decision, error) {
	if err := ctx.Err(); err != nil {
		return Decision{}, err
	}
	e.mu.Lock()
	if e.schedule == nil {
		defer e.mu.Unlock()
		return e.process(req, resp)
	}
	timeout, _ := time.ParseDuration(e.schedule.Timeout)
	e.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for {
		if err := ctx.Err(); err != nil {
			return Decision{}, err
		}
		e.mu.Lock()
		if e.scheduleFailed {
			e.mu.Unlock()
			return Decision{}, fmt.Errorf("schedule already failed")
		}
		op := e.inspectRequest(req)
		if e.schedulePosition >= len(e.schedule.Steps) {
			if e.schedule.Partial {
				d, err := e.process(req, resp)
				e.mu.Unlock()
				return d, err
			}
			e.scheduleFailed = true
			e.mu.Unlock()
			return Decision{}, fmt.Errorf("unexpected call after schedule completed")
		}
		selector, _ := compileSelector(e.schedule.Steps[e.schedulePosition])
		if selector.matches(req, op) {
			e.schedulePosition++
			d, err := e.process(req, resp)
			if err != nil {
				e.scheduleFailed = true
			}
			close(e.scheduleChanged)
			e.scheduleChanged = make(chan struct{})
			e.mu.Unlock()
			return d, err
		}
		possible := false
		for _, step := range e.schedule.Steps[e.schedulePosition+1:] {
			s, _ := compileSelector(step)
			if s.matches(req, op) {
				possible = true
				break
			}
		}
		if !possible {
			if e.schedule.Partial {
				d, err := e.process(req, resp)
				e.mu.Unlock()
				return d, err
			}
			e.scheduleFailed = true
			close(e.scheduleChanged)
			e.scheduleChanged = make(chan struct{})
			e.mu.Unlock()
			return Decision{}, fmt.Errorf("call does not match remaining schedule")
		}
		changed := e.scheduleChanged
		e.mu.Unlock()
		select {
		case <-ctx.Done():
			e.mu.Lock()
			e.scheduleFailed = true
			close(e.scheduleChanged)
			e.scheduleChanged = make(chan struct{})
			e.mu.Unlock()
			return Decision{}, fmt.Errorf("schedule wait: %w", ctx.Err())
		case <-changed:
		}
	}
}
