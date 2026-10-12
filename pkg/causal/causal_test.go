package causal

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"sync"
	"testing"
	"time"

	"infernosim/pkg/agentotel"
)

func independent(t *testing.T, n int) Graph {
	t.Helper()
	var xs []Exchange
	for i := 0; i < n; i++ {
		id := string(rune('a' + i))
		if i >= 26 {
			id = fmt.Sprintf("op%d", i)
		}
		xs = append(xs, Exchange{ID: id, Protocol: "http", Method: "POST", Host: "test", Path: "/" + id, Record: id, Request: []byte(`{}`), Response: []byte(`{}`)})
	}
	g, err := Discover(xs, nil, Declarations{Review: "test fixture has independent requests"})
	if err != nil {
		t.Fatal(err)
	}
	return g
}

// This oracle permutes all boundaries then filters constraints; it shares no
// ready-set or in-degree logic with the production topological planner.
func oracle(g Graph) map[string]bool {
	nodes := g.Nodes()
	out := map[string]bool{}
	var permute func(int)
	permute = func(i int) {
		if i == len(nodes) {
			positions := map[string]int{}
			for j, n := range nodes {
				positions[n] = j
			}
			for _, e := range g.Edges {
				if positions[e.From] >= positions[e.To] {
					return
				}
			}
			out[Hash(nodes)] = true
			return
		}
		for j := i; j < len(nodes); j++ {
			nodes[i], nodes[j] = nodes[j], nodes[i]
			permute(i + 1)
			nodes[i], nodes[j] = nodes[j], nodes[i]
		}
	}
	permute(0)
	return out
}

func TestPlannerAgainstIndependentOracle(t *testing.T) {
	for _, linked := range []bool{false, true} {
		g := independent(t, 3)
		if linked {
			g.AddEdge(Release("a"), Admit("c"), Evidence{Rule: "test", Source: "fixture", SHA256: Digest(nil)})
		}
		want := oracle(g)
		p, err := Generate(g, 17, 1000)
		if err != nil {
			t.Fatal(err)
		}
		got := map[string]bool{}
		for _, s := range p.Schedules {
			if err := s.Validate(g); err != nil {
				t.Fatal(err)
			}
			got[Hash(s.Order)] = true
		}
		if !p.Exhaustive || p.Truncated || !reflect.DeepEqual(got, want) {
			t.Fatalf("oracle mismatch: %d vs %d, plan %#v", len(got), len(want), p)
		}
		again, _ := Generate(g, 17, 1000)
		if !reflect.DeepEqual(p, again) {
			t.Fatal("same seed changed plan")
		}
		other, _ := Generate(g, 18, 1000)
		if reflect.DeepEqual(p.Schedules, other.Schedules) {
			t.Fatal("seed did not change traversal")
		}
	}
}

func TestPlannerBudgetAndLargeGraph(t *testing.T) {
	g := independent(t, 64)
	p, err := Generate(g, 91, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Schedules) != 5 || !p.Truncated || p.Exhaustive || p.Attempts > 101 {
		t.Fatalf("unbounded or inaccurate plan: %#v", p)
	}
	for _, s := range p.Schedules {
		if err := s.Validate(g); err != nil {
			t.Fatal(err)
		}
	}
	g = independent(t, 2)
	g.AddEdge(Release("a"), Admit("b"), Evidence{Rule: "test", Source: "fixture", SHA256: Digest(nil)})
	g.AddEdge(Release("b"), Admit("a"), Evidence{Rule: "test", Source: "fixture", SHA256: Digest(nil)})
	if _, err := Generate(g, 0, 5); err == nil {
		t.Fatal("accepted cycle")
	}
}

func TestDiscoverFanOutInAndParentBoundaries(t *testing.T) {
	xs := []Exchange{
		{ID: "model", Protocol: "http", Record: "1", Request: []byte(`{}`), Response: []byte(`{"output":[{"type":"function_call","call_id":"tc1","name":"one","arguments":"{}"},{"type":"function_call","call_id":"tc2","name":"two","arguments":"{}"}]}`)},
		{ID: "one", Protocol: "mcp", Record: "2", Request: []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"one","arguments":{}}}`), TraceID: "t", SpanID: "child"},
		{ID: "two", Protocol: "mcp", Record: "3", Request: []byte(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"two","arguments":{}}}`)},
		{ID: "next", Protocol: "http", Record: "4", Request: []byte(`{"input":[{"type":"function_call_output","call_id":"tc1","output":"ok"},{"type":"function_call_output","call_id":"tc2","output":"ok"}]}`)},
	}
	spans := []agentotel.Span{{TraceID: "t", SpanID: "parent"}, {TraceID: "t", SpanID: "child", ParentSpanID: "parent"}}
	g, err := Discover(xs, spans, Declarations{Review: "reviewed", SpanOperations: map[string]string{"t/parent": "model"}})
	if err != nil {
		t.Fatal(err)
	}
	edges := map[string]bool{}
	for _, e := range g.Edges {
		edges[e.From+">"+e.To] = true
		if len(e.Evidence) == 0 {
			t.Fatal("missing evidence")
		}
	}
	for _, e := range []string{"model:release>one:admit", "model:release>two:admit", "one:release>next:admit", "two:release>next:admit", "model:admit>one:admit"} {
		if !edges[e] {
			t.Fatal("missing", e)
		}
	}
	if edges["one:release>two:admit"] || edges["two:release>one:admit"] {
		t.Fatal("invented sibling ordering")
	}
	if err := g.Validate(true); err != nil {
		t.Fatal(err)
	}
}

func TestDiscoveryAmbiguityAndMissingEvidence(t *testing.T) {
	g := independent(t, 1)
	g.Review = ""
	if _, err := Generate(g, 0, 1); err == nil {
		t.Fatal("allowed unreviewed graph")
	}
	x := Exchange{ID: "a", Protocol: "mcp", Record: "1", Request: []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"x"}}`)}
	y := x
	y.ID = "b"
	y.Record = "2"
	y.Request = []byte(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"x"}}`)
	g, err := Discover([]Exchange{x, y}, nil, Declarations{Review: "reviewed"})
	if err != nil {
		t.Fatal(err)
	}
	if g.Validate(true) == nil {
		t.Fatal("identical calls accepted without identity")
	}
	g, err = Discover([]Exchange{x, y}, nil, Declarations{Review: "reviewed", Identities: map[string]string{"a": "first", "b": "second"}})
	if err != nil {
		t.Fatal(err)
	}
	if err = g.Validate(true); err != nil {
		t.Fatal(err)
	}
	spans := []agentotel.Span{{TraceID: "t", SpanID: "c", ParentSpanID: "absent"}}
	x.TraceID = "t"
	x.SpanID = "c"
	g, err = Discover([]Exchange{x}, spans, Declarations{Review: "reviewed"})
	if err != nil {
		t.Fatal(err)
	}
	if g.Validate(true) == nil {
		t.Fatal("missing parent concealed")
	}
}

func TestControllerConcurrentAdmissionAndRelease(t *testing.T) {
	g := independent(t, 3)
	order := []string{Admit("b"), Admit("a"), Release("a"), Admit("c"), Release("c"), Release("b")}
	s := NewSchedule(g, order)
	c, err := NewController(g, s, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	var observed []string
	for _, op := range g.Operations {
		wg.Add(1)
		go func(o Operation) {
			defer wg.Done()
			if _, err := c.Bind(o.Fingerprint, "", fmt.Sprintf("%q", o.ID)); err != nil {
				t.Error(err)
				return
			}
			for _, n := range []string{Admit(o.ID), Release(o.ID)} {
				if err := c.Step(context.Background(), n, func() error { mu.Lock(); observed = append(observed, n); mu.Unlock(); return nil }); err != nil {
					t.Error(err)
					return
				}
			}
		}(op)
	}
	wg.Wait()
	if err := c.Complete(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(observed, order) || !reflect.DeepEqual(c.Snapshot().Observed, order) {
		t.Fatalf("order %v", observed)
	}
	if _, err := c.Bind(g.Operations[0].Fingerprint, "", ""); err == nil {
		t.Fatal("accepted repeated participant")
	}
}

func TestControllerTimeoutCancellationAndFailedDelivery(t *testing.T) {
	for _, mode := range []string{"timeout", "cancel", "delivery"} {
		t.Run(mode, func(t *testing.T) {
			g := independent(t, 1)
			s := NewSchedule(g, []string{Admit("a"), Release("a")})
			c, _ := NewController(g, s, 10*time.Millisecond)
			_, _ = c.Bind(g.Operations[0].Fingerprint, "", "")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			node := Release("a")
			fn := func() error { return nil }
			if mode == "cancel" {
				cancel()
			}
			if mode == "delivery" {
				node = Admit("a")
				fn = func() error { return errors.New("write failed") }
			}
			if c.Step(ctx, node, fn) == nil {
				t.Fatal("accepted failure")
			}
			if c.Snapshot().Failure == "" || c.Complete() == nil {
				t.Fatal("missing invalid evidence")
			}
		})
	}
}

func TestIdentityCanonicalizationPreservesNumbers(t *testing.T) {
	a, _, id := RequestFingerprint("mcp", "STDIO", "stdio.mcp", "/stdio", []byte(`{"jsonrpc":"2.0","id":9007199254740993,"method":"tools/call","params":{"arguments":{"n":9007199254740993},"_meta":{"infernosim/operation":"one"}}}`))
	b, identity, _ := RequestFingerprint("mcp", "STDIO", "stdio.mcp", "/stdio", []byte(`{"params":{"arguments":{"n":9007199254740993},"_meta":{"infernosim/operation":"one"}},"method":"tools/call","id":"different","jsonrpc":"2.0"}`))
	if a != b || id != "9007199254740993" || identity != "one" {
		t.Fatal("unstable canonical identity")
	}
	r, err := RemapResponse([]byte(`{"jsonrpc":"2.0","id":1,"result":{"n":9007199254740993}}`), `"new"`)
	if err != nil {
		t.Fatal(err)
	}
	v, _ := Decode(r)
	if obj(v)["id"] != "new" {
		t.Fatal("not remapped")
	}
}

func FuzzPlanner(f *testing.F) {
	f.Add([]byte{1, 2, 3}, int64(7))
	f.Fuzz(func(t *testing.T, data []byte, seed int64) {
		g := independent(t, 3)
		nodes := g.Nodes()
		sort.Strings(nodes)
		for i, b := range data {
			if i > 20 {
				break
			}
			a := int(b) % len(nodes)
			z := (a + 1) % len(nodes)
			if a < z {
				g.AddEdge(nodes[a], nodes[z], Evidence{Rule: "fuzz", Source: "fixture", SHA256: Digest(data)})
			}
		}
		if g.Validate(true) != nil {
			return
		}
		p, err := Generate(g, seed, 5)
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range p.Schedules {
			if err := s.Validate(g); err != nil {
				t.Fatal(err)
			}
		}
	})
}
