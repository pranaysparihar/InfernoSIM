package causal

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"
)

type Binding struct {
	Operation     string `json:"operation"`
	RuntimeIDHash string `json:"runtime_id_hash,omitempty"`
}

type Replay struct {
	Graph    Graph    `json:"graph" yaml:"graph"`
	Schedule Schedule `json:"schedule" yaml:"schedule"`
	Timeout  string   `json:"timeout" yaml:"timeout"`
}

func (r Replay) Controller() (*Controller, error) {
	d, err := time.ParseDuration(r.Timeout)
	if err != nil {
		return nil, fmt.Errorf("causal timeout: %w", err)
	}
	return NewController(r.Graph, r.Schedule, d)
}

type Execution struct {
	ScheduleID string    `json:"schedule_id"`
	GraphHash  string    `json:"graph_hash"`
	Observed   []string  `json:"observed"`
	Bindings   []Binding `json:"bindings"`
	Completed  bool      `json:"completed"`
	Failure    string    `json:"failure,omitempty"`
}

func (e Execution) Validate(g Graph, s Schedule) error {
	if err := s.Validate(g); err != nil {
		return err
	}
	if e.Failure != "" || !e.Completed || e.GraphHash != s.GraphHash || e.ScheduleID != s.ID || !reflect.DeepEqual(e.Observed, s.Order) {
		return fmt.Errorf("causal proof does not show the exact completed schedule")
	}
	if len(e.Bindings) != len(g.Operations) {
		return fmt.Errorf("causal proof is missing identity bindings")
	}
	ids := map[string]bool{}
	for _, o := range g.Operations {
		ids[o.ID] = true
	}
	for _, b := range e.Bindings {
		if !ids[b.Operation] {
			return fmt.Errorf("causal proof has an unknown or duplicate binding")
		}
		delete(ids, b.Operation)
		if b.RuntimeIDHash != "" && !validHash.MatchString(b.RuntimeIDHash) {
			return fmt.Errorf("invalid runtime identity digest")
		}
	}
	return nil
}

// Controller uses one contract for every transport. A boundary is recorded only
// after its callback succeeds; callbacks execute without holding the state lock.
// Transports must keep receiving input while workers await a boundary.
type Controller struct {
	mu        sync.Mutex
	graph     Graph
	schedule  Schedule
	timeout   time.Duration
	changed   chan struct{}
	bound     map[string]bool
	active    bool
	execution Execution
}

func NewController(g Graph, s Schedule, timeout time.Duration) (*Controller, error) {
	if err := s.Validate(g); err != nil {
		return nil, err
	}
	if timeout <= 0 || timeout > time.Minute {
		return nil, fmt.Errorf("causal boundary timeout must be > 0 and <= 60s")
	}
	return &Controller{graph: g, schedule: s, timeout: timeout, changed: make(chan struct{}), bound: map[string]bool{}, execution: Execution{ScheduleID: s.ID, GraphHash: s.GraphHash}}, nil
}

func (c *Controller) signal() { close(c.changed); c.changed = make(chan struct{}) }
func (c *Controller) fail(err error) error {
	if c.execution.Failure == "" {
		c.execution.Failure = err.Error()
		c.signal()
	}
	return fmt.Errorf("causal execution invalid: %s", c.execution.Failure)
}
func (c *Controller) Fail(err error) error { c.mu.Lock(); defer c.mu.Unlock(); return c.fail(err) }

func (c *Controller) Bind(fingerprint, identity, runtimeID string) (Operation, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if runtimeID != "" && !validRPCID(runtimeID) {
		return Operation{}, c.fail(fmt.Errorf("runtime JSON-RPC ID must be a nonempty string or integer"))
	}
	if c.execution.Failure != "" {
		return Operation{}, fmt.Errorf("causal execution invalid: %s", c.execution.Failure)
	}
	var candidates []Operation
	for _, o := range c.graph.Operations {
		if o.Fingerprint == fingerprint && (o.Identity == "" || o.Identity == identity) {
			candidates = append(candidates, o)
		}
	}
	if len(candidates) != 1 {
		return Operation{}, c.fail(fmt.Errorf("request has %d causal identity matches; supply a stable identity for ambiguous calls", len(candidates)))
	}
	o := candidates[0]
	if c.bound[o.ID] {
		return Operation{}, c.fail(fmt.Errorf("duplicate participant %s", o.ID))
	}
	c.bound[o.ID] = true
	b := Binding{Operation: o.ID}
	if runtimeID != "" {
		b.RuntimeIDHash = Digest([]byte(runtimeID))
	}
	c.execution.Bindings = append(c.execution.Bindings, b)
	return o, nil
}

func (c *Controller) Step(ctx context.Context, node string, fn func() error) error {
	return c.At(ctx, node, func(context.Context) error { return fn() })
}

// At provides the bounded boundary context to transports for blocking writes.
func (c *Controller) At(ctx context.Context, node string, fn func(context.Context) error) error {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	for {
		c.mu.Lock()
		if c.execution.Failure != "" {
			err := fmt.Errorf("causal execution invalid: %s", c.execution.Failure)
			c.mu.Unlock()
			return err
		}
		if err := ctx.Err(); err != nil {
			err = c.fail(fmt.Errorf("waiting for %s: %w", node, err))
			c.mu.Unlock()
			return err
		}
		pos := len(c.execution.Observed)
		op := strings.TrimSuffix(strings.TrimSuffix(node, ":admit"), ":release")
		if !c.bound[op] {
			err := c.fail(fmt.Errorf("unbound causal participant %s", op))
			c.mu.Unlock()
			return err
		}
		remaining := false
		for _, n := range c.schedule.Order[pos:] {
			if n == node {
				remaining = true
				break
			}
		}
		if !remaining {
			err := c.fail(fmt.Errorf("unexpected or repeated boundary %s", node))
			c.mu.Unlock()
			return err
		}
		if !c.active && c.schedule.Order[pos] == node {
			c.active = true
			c.mu.Unlock()
			err := fn(ctx)
			c.mu.Lock()
			c.active = false
			if err == nil {
				err = ctx.Err()
			}
			if err != nil {
				err = c.fail(fmt.Errorf("boundary %s: %w", node, err))
			} else if c.execution.Failure != "" {
				err = fmt.Errorf("causal execution invalid: %s", c.execution.Failure)
			} else {
				c.execution.Observed = append(c.execution.Observed, node)
				c.execution.Completed = len(c.execution.Observed) == len(c.schedule.Order)
				c.signal()
			}
			c.mu.Unlock()
			return err
		}
		changed := c.changed
		c.mu.Unlock()
		select {
		case <-ctx.Done():
		case <-changed:
		}
	}
}

func (c *Controller) Snapshot() Execution {
	c.mu.Lock()
	defer c.mu.Unlock()
	r := c.execution
	r.Observed = append([]string(nil), r.Observed...)
	r.Bindings = append([]Binding(nil), r.Bindings...)
	// Bind arrival order is intentionally not part of deterministic proof order.
	for i := 0; i < len(r.Bindings); i++ {
		for j := i + 1; j < len(r.Bindings); j++ {
			if r.Bindings[j].Operation < r.Bindings[i].Operation {
				r.Bindings[i], r.Bindings[j] = r.Bindings[j], r.Bindings[i]
			}
		}
	}
	return r
}

func (c *Controller) Complete() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.execution.Failure != "" {
		return fmt.Errorf("causal execution invalid: %s", c.execution.Failure)
	}
	if !c.execution.Completed {
		return c.fail(fmt.Errorf("missing participants or boundaries: completed %d of %d", len(c.execution.Observed), len(c.schedule.Order)))
	}
	return nil
}

func (c *Controller) Verify(o Operation, x Exchange) error { return VerifyExchange(c.graph, o, x) }
