package causal

import (
	"fmt"
	"math/rand"
	"sort"
)

type Schedule struct {
	ID        string   `json:"id" yaml:"id"`
	GraphHash string   `json:"graph_hash" yaml:"graph_hash"`
	Order     []string `json:"order" yaml:"order"`
}

type Plan struct {
	Version    int        `json:"version"`
	GraphHash  string     `json:"graph_hash"`
	Seed       int64      `json:"seed"`
	Budget     int        `json:"budget"`
	Attempts   int        `json:"attempts"`
	Duplicates int        `json:"duplicates"`
	Truncated  bool       `json:"truncated"`
	Exhaustive bool       `json:"exhaustive"`
	Schedules  []Schedule `json:"schedules"`
}

func NewSchedule(g Graph, order []string) Schedule {
	gh := Hash(g)
	return Schedule{ID: "causal_" + Hash(struct {
		Graph string
		Order []string
	}{gh, order})[:20], GraphHash: gh, Order: append([]string(nil), order...)}
}
func (s Schedule) Validate(g Graph) error {
	if s.GraphHash != Hash(g) || s.ID != NewSchedule(g, s.Order).ID {
		return fmt.Errorf("causal schedule graph hash or ID mismatch")
	}
	return g.ValidateOrder(s.Order)
}

// Generate enumerates small graphs, and samples larger graphs with a bounded
// number of seeded Kahn traversals. Sampled plans never claim exhaustive coverage.
func Generate(g Graph, seed int64, budget int) (Plan, error) {
	p := Plan{Version: Version, GraphHash: Hash(g), Seed: seed, Budget: budget}
	if budget < 1 || budget > 1000 {
		return p, fmt.Errorf("causal schedule budget must be 1..1000")
	}
	if err := g.Validate(true); err != nil {
		return p, err
	}
	nodes := g.Nodes()
	adj := map[string][]string{}
	degree := map[string]int{}
	for _, e := range g.Edges {
		adj[e.From] = append(adj[e.From], e.To)
		degree[e.To]++
	}
	for n := range adj {
		sort.Strings(adj[n])
	}
	rng := rand.New(rand.NewSource(seed))
	seen := map[string]bool{}
	add := func(order []string) bool {
		p.Attempts++
		s := NewSchedule(g, order)
		if seen[s.ID] {
			p.Duplicates++
			return true
		}
		seen[s.ID] = true
		if len(p.Schedules) == budget {
			p.Truncated = true
			return false
		}
		p.Schedules = append(p.Schedules, s)
		return true
	}
	if len(nodes) <= 12 {
		used := map[string]bool{}
		var walk func([]string) bool
		walk = func(order []string) bool {
			if len(order) == len(nodes) {
				return add(order)
			}
			var ready []string
			for _, n := range nodes {
				if !used[n] && degree[n] == 0 {
					ready = append(ready, n)
				}
			}
			rng.Shuffle(len(ready), func(i, j int) { ready[i], ready[j] = ready[j], ready[i] })
			for _, n := range ready {
				used[n] = true
				for _, next := range adj[n] {
					degree[next]--
				}
				ok := walk(append(order, n))
				for _, next := range adj[n] {
					degree[next]++
				}
				delete(used, n)
				if !ok {
					return false
				}
			}
			return true
		}
		p.Exhaustive = walk(nil)
		return p, nil
	}
	// Linear graphs can be certified exhaustive without exploring alternatives.
	unique := true
	for attempt := 0; attempt < budget*20+1; attempt++ {
		d := map[string]int{}
		var ready, order []string
		for _, n := range nodes {
			d[n] = degree[n]
			if d[n] == 0 {
				ready = append(ready, n)
			}
		}
		for len(ready) > 0 {
			if len(ready) > 1 {
				unique = false
			}
			i := rng.Intn(len(ready))
			n := ready[i]
			ready = append(ready[:i], ready[i+1:]...)
			order = append(order, n)
			for _, next := range adj[n] {
				d[next]--
				if d[next] == 0 {
					ready = append(ready, next)
				}
			}
			sort.Strings(ready)
		}
		if !add(order) {
			return p, nil
		}
		if unique {
			p.Exhaustive = true
			return p, nil
		}
	}
	p.Truncated = true
	return p, nil
}
