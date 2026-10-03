package agentrunner

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand"
	"sort"

	"infernosim/pkg/agentreliability"
)

type ExploreOptions struct {
	Seed            int64  `json:"seed"`
	Budget          int    `json:"budget"`
	MaxFaults       int    `json:"max_faults"`
	Occurrences     int    `json:"occurrences"`
	PermuteSchedule string `json:"permute_schedule,omitempty"`
}

// Trial modifies only explicitly selected fault occurrences and admission
// ordering. It cannot introduce requests or invent recorded model responses.
type Trial struct {
	Case        agentreliability.Case      `json:"case"`
	Occurrences map[string]int             `json:"occurrences,omitempty"`
	Schedule    *agentreliability.Schedule `json:"schedule,omitempty"`
}
type ExplorationPlan struct {
	Version   int            `json:"version"`
	Options   ExploreOptions `json:"options"`
	Truncated bool           `json:"truncated"`
	Trials    []Trial        `json:"trials"`
}

func cloneAgent(config agentreliability.Config) (agentreliability.Config, error) {
	b, err := json.Marshal(config)
	if err != nil {
		return agentreliability.Config{}, fmt.Errorf("configuration must contain JSON-compatible values: %w", err)
	}
	var c agentreliability.Config
	decoder := json.NewDecoder(bytes.NewReader(b))
	decoder.UseNumber()
	err = decoder.Decode(&c)
	return c, err
}
func (t Trial) AgentConfig(base agentreliability.Config) (agentreliability.Config, error) {
	c, err := cloneAgent(base)
	if err != nil {
		return c, err
	}
	found := map[string]bool{}
	for i := range c.Faults {
		if n, ok := t.Occurrences[c.Faults[i].ID]; ok {
			if n < 0 || n > agentreliability.MaximumCalls {
				return c, fmt.Errorf("invalid occurrence")
			}
			c.Faults[i].Select.Occurrence = n
			found[c.Faults[i].ID] = true
		}
	}
	if len(found) != len(t.Occurrences) {
		return c, fmt.Errorf("unknown occurrence override")
	}
	if t.Schedule != nil {
		if t.Case.ScheduleID != t.Schedule.ID {
			return c, fmt.Errorf("schedule ID mismatch")
		}
		c.Schedules = []agentreliability.Schedule{*t.Schedule}
	} else if t.Case.ScheduleID != "" {
		return c, fmt.Errorf("missing trial schedule")
	}
	if err := c.Validate(); err != nil {
		return c, err
	}
	known := map[string]bool{}
	for _, f := range c.Faults {
		known[f.ID] = true
	}
	for _, id := range t.Case.ActiveFaults() {
		if !known[id] {
			return c, fmt.Errorf("unknown trial fault")
		}
	}
	return c, nil
}
func identifyTrial(t Trial, scope string, seed int64) Trial {
	t.Case.ID = ""
	hash := agentreliability.StableHash(struct {
		Scope string
		Seed  int64
		Trial Trial
	}{scope, seed, t})
	t.Case.ID = "explore_" + hash[:20]
	return t
}

// PlanExploration deterministically enumerates baseline, schedule variations,
// fault subsets up to depth four, and occurrence positions. The case budget is
// strict; Truncated records that candidates remain, never a coverage claim.
func PlanExploration(config agentreliability.Config, scope string, o ExploreOptions) (ExplorationPlan, error) {
	p := ExplorationPlan{Version: 1, Options: o}
	if o.Budget < 1 || o.Budget > 1000 || o.MaxFaults < 1 || o.MaxFaults > 4 || o.Occurrences < 0 || o.Occurrences > 16 {
		return p, fmt.Errorf("exploration requires budget 1..1000, max-faults 1..4, occurrences 0..16")
	}
	if len(config.Faults) > 32 {
		return p, fmt.Errorf("exploration supports at most 32 declared faults")
	}
	if err := config.Validate(); err != nil {
		return p, err
	}
	var err error
	config, err = cloneAgent(config)
	if err != nil {
		return p, err
	}
	sort.Slice(config.Faults, func(i, j int) bool { return config.Faults[i].ID < config.Faults[j].ID })
	rng := rand.New(rand.NewSource(o.Seed))
	rng.Shuffle(len(config.Faults), func(i, j int) { config.Faults[i], config.Faults[j] = config.Faults[j], config.Faults[i] })
	schedules := []*agentreliability.Schedule{nil}
	seenSchedules := map[string]bool{}
	addSchedule := func(s agentreliability.Schedule) {
		key := agentreliability.StableHash(s)
		if !seenSchedules[key] {
			seenSchedules[key] = true
			cp := s
			cp.Steps = append([]agentreliability.Selector(nil), s.Steps...)
			schedules = append(schedules, &cp)
		}
	}
	matched := o.PermuteSchedule == ""
	for _, s := range config.Schedules {
		addSchedule(s)
		if s.ID != o.PermuteSchedule {
			continue
		}
		matched = true
		if len(s.Steps) > 6 {
			return p, fmt.Errorf("schedule permutation supports at most six explicitly independent calls")
		}
		var permute func(int)
		permute = func(i int) {
			if i == len(s.Steps) {
				addSchedule(s)
				return
			}
			for j := i; j < len(s.Steps); j++ {
				s.Steps[i], s.Steps[j] = s.Steps[j], s.Steps[i]
				permute(i + 1)
				s.Steps[i], s.Steps[j] = s.Steps[j], s.Steps[i]
			}
		}
		permute(0)
	}
	if !matched {
		return p, fmt.Errorf("unknown schedule selected for permutation")
	}
	seen := map[string]bool{}
	add := func(t Trial) bool {
		t.Case.Category = "exploration"
		t.Case.Severity = "high"
		t.Case.Description = "bounded fault and admission exploration"
		if len(t.Case.ActiveFaults()) == 0 && t.Schedule == nil {
			t.Case.Description = "recorded baseline"
		}
		if t.Schedule != nil {
			t.Case.ScheduleID = t.Schedule.ID
		}
		t = identifyTrial(t, scope, o.Seed)
		if seen[t.Case.ID] {
			return true
		}
		seen[t.Case.ID] = true
		if len(p.Trials) >= o.Budget {
			p.Truncated = true
			return false
		}
		p.Trials = append(p.Trials, t)
		return true
	}
	if !add(Trial{}) {
		return p, nil
	}
	// Cover individual faults before spending the budget on permutations/pairs.
	for _, f := range config.Faults {
		if !add(Trial{Case: agentreliability.Case{FaultIDs: []string{f.ID}}}) {
			return p, nil
		}
	}
	for _, s := range schedules[1:] {
		if !add(Trial{Schedule: s}) {
			return p, nil
		}
	}
	var enumerate func([]agentreliability.Fault) bool
	enumerate = func(fs []agentreliability.Fault) bool {
		ids := []string{}
		for _, f := range fs {
			ids = append(ids, f.ID)
		}
		sort.Strings(ids)
		positions := map[string]int{}
		var variants func(int) bool
		variants = func(i int) bool {
			if i == len(fs) {
				for _, s := range schedules {
					cp := map[string]int{}
					for k, v := range positions {
						cp[k] = v
					}
					if !add(Trial{Case: agentreliability.Case{FaultIDs: ids}, Occurrences: cp, Schedule: s}) {
						return false
					}
				}
				return true
			}
			if !variants(i + 1) {
				return false
			}
			for n := 1; n <= o.Occurrences; n++ {
				if n == fs[i].Select.Occurrence {
					continue
				}
				positions[fs[i].ID] = n
				if !variants(i + 1) {
					return false
				}
			}
			delete(positions, fs[i].ID)
			return true
		}
		return variants(0)
	}
	for size := 1; size <= o.MaxFaults && size <= len(config.Faults); size++ {
		var subsets func(int, []agentreliability.Fault) bool
		subsets = func(start int, chosen []agentreliability.Fault) bool {
			if len(chosen) == size {
				return enumerate(chosen)
			}
			for i := start; i <= len(config.Faults)-(size-len(chosen)); i++ {
				if !subsets(i+1, append(chosen, config.Faults[i])) {
					return false
				}
			}
			return true
		}
		if !subsets(0, nil) {
			return p, nil
		}
	}
	return p, nil
}

func RunTrial(ctxOpts Options, base agentreliability.Config, t Trial) (Options, error) {
	c, err := t.AgentConfig(base)
	if err != nil {
		return ctxOpts, err
	}
	ctxOpts.AgentConfig = &c
	ctxOpts.Case = t.Case
	return ctxOpts, nil
}
