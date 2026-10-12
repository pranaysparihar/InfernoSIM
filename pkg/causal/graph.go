// Package causal describes evidence-backed ordering at recorded protocol
// boundaries. It does not infer hidden application synchronization from time.
package causal

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
)

const (
	Version           = 1
	MaximumOperations = 256
	MaximumEdges      = 8192
)

type Operation struct {
	ID          string `json:"id" yaml:"id"`
	Protocol    string `json:"protocol" yaml:"protocol"`
	Fingerprint string `json:"fingerprint" yaml:"fingerprint"`
	Record      string `json:"record" yaml:"record"`
	RequestID   string `json:"request_id,omitempty" yaml:"request_id,omitempty"`
	// Identity is an explicit application-supplied stable identity. It is required
	// for indistinguishable requests; arrival order is never an identity.
	Identity           string `json:"identity,omitempty" yaml:"identity,omitempty"`
	Notification       bool   `json:"notification,omitempty" yaml:"notification,omitempty"`
	ServerNotification bool   `json:"server_notification,omitempty" yaml:"server_notification,omitempty"`
}

type Evidence struct {
	Rule   string `json:"rule" yaml:"rule"`
	Source string `json:"source" yaml:"source"`
	SHA256 string `json:"sha256" yaml:"sha256"`
	Detail string `json:"detail,omitempty" yaml:"detail,omitempty"`
}

type Edge struct {
	From     string     `json:"from" yaml:"from"`
	To       string     `json:"to" yaml:"to"`
	Evidence []Evidence `json:"evidence" yaml:"evidence"`
}

type Issue struct {
	ID         string `json:"id" yaml:"id"`
	Kind       string `json:"kind" yaml:"kind"`
	Detail     string `json:"detail" yaml:"detail"`
	Blocking   bool   `json:"blocking" yaml:"blocking"`
	Resolution string `json:"resolution,omitempty" yaml:"resolution,omitempty"`
}

type Graph struct {
	Version    int         `json:"version" yaml:"version"`
	Operations []Operation `json:"operations" yaml:"operations"`
	Edges      []Edge      `json:"edges" yaml:"edges"`
	Issues     []Issue     `json:"issues,omitempty" yaml:"issues,omitempty"`
	// Review records the user's acknowledgement that the recording cannot prove
	// the absence of dependencies outside the supported protocol boundaries.
	Review string `json:"review,omitempty" yaml:"review,omitempty"`
}

func Admit(id string) string   { return id + ":admit" }
func Release(id string) string { return id + ":release" }
func Hash(v any) string        { b, _ := json.Marshal(v); return Digest(b) }
func Digest(b []byte) string   { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

func (g Graph) Nodes() []string {
	r := make([]string, 0, 2*len(g.Operations))
	for _, o := range g.Operations {
		r = append(r, Admit(o.ID), Release(o.ID))
	}
	sort.Strings(r)
	return r
}

var validID = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$`)
var validHash = regexp.MustCompile(`^[0-9a-f]{64}$`)

// Validate rejects malformed evidence and cycles even before review. Requiring
// review additionally rejects unresolved discovery issues and ambiguous calls.
func (g Graph) Validate(requireReview bool) error {
	if g.Version != Version || len(g.Operations) == 0 || len(g.Operations) > MaximumOperations || len(g.Edges) > MaximumEdges {
		return fmt.Errorf("causal graph requires version %d, 1..%d operations and at most %d edges", Version, MaximumOperations, MaximumEdges)
	}
	nodes := map[string]bool{}
	identities := map[string]bool{}
	fingerprints := map[string][]Operation{}
	for _, o := range g.Operations {
		if o.RequestID != "" && !validRPCID(o.RequestID) {
			return fmt.Errorf("invalid recorded JSON-RPC ID for %s", o.ID)
		}
		if !validID.MatchString(o.ID) || nodes[Admit(o.ID)] || !validHash.MatchString(o.Fingerprint) || o.Record == "" || (o.Protocol != "http" && o.Protocol != "mcp") {
			return fmt.Errorf("invalid or duplicate causal operation %q", o.ID)
		}
		if o.Identity != "" && identities[o.Identity] {
			return fmt.Errorf("duplicate causal identity %q", o.Identity)
		}
		identities[o.Identity] = true
		nodes[Admit(o.ID)], nodes[Release(o.ID)] = true, true
		fingerprints[o.Fingerprint] = append(fingerprints[o.Fingerprint], o)
	}
	if requireReview {
		if g.Review == "" {
			return fmt.Errorf("causal graph needs a review acknowledging unobserved application dependencies")
		}
		for _, group := range fingerprints {
			if len(group) > 1 {
				for _, o := range group {
					if o.Identity == "" {
						return fmt.Errorf("ambiguous identical request %s needs a stable identity", o.ID)
					}
				}
			}
		}
		for _, issue := range g.Issues {
			if issue.Blocking && issue.Resolution == "" {
				return fmt.Errorf("unresolved causal issue %s: %s", issue.ID, issue.Detail)
			}
		}
	}
	adj, degree := map[string][]string{}, map[string]int{}
	seen := map[string]bool{}
	for _, edge := range g.Edges {
		key := edge.From + "\x00" + edge.To
		if !nodes[edge.From] || !nodes[edge.To] || edge.From == edge.To || seen[key] || len(edge.Evidence) == 0 {
			return fmt.Errorf("invalid, duplicate or unsupported causal edge %s -> %s", edge.From, edge.To)
		}
		for _, e := range edge.Evidence {
			if e.Rule == "" || e.Source == "" || !validHash.MatchString(e.SHA256) {
				return fmt.Errorf("edge %s -> %s has invalid evidence", edge.From, edge.To)
			}
		}
		seen[key] = true
		adj[edge.From] = append(adj[edge.From], edge.To)
		degree[edge.To]++
	}
	for _, o := range g.Operations {
		if !seen[Admit(o.ID)+"\x00"+Release(o.ID)] {
			return fmt.Errorf("operation %s is missing its request/response edge", o.ID)
		}
	}
	var ready []string
	for n := range nodes {
		if degree[n] == 0 {
			ready = append(ready, n)
		}
	}
	visited := 0
	for len(ready) > 0 {
		n := ready[len(ready)-1]
		ready = ready[:len(ready)-1]
		visited++
		for _, next := range adj[n] {
			degree[next]--
			if degree[next] == 0 {
				ready = append(ready, next)
			}
		}
	}
	if visited != len(nodes) {
		return fmt.Errorf("causal graph contains a cycle")
	}
	return nil
}

func (g Graph) ValidateOrder(order []string) error {
	if err := g.Validate(true); err != nil {
		return err
	}
	if len(order) != 2*len(g.Operations) {
		return fmt.Errorf("schedule must contain every admission and release exactly once")
	}
	pos := map[string]int{}
	for i, n := range order {
		if _, ok := pos[n]; ok {
			return fmt.Errorf("schedule repeats %s", n)
		}
		pos[n] = i
	}
	for _, n := range g.Nodes() {
		if _, ok := pos[n]; !ok {
			return fmt.Errorf("schedule is missing %s", n)
		}
	}
	for _, e := range g.Edges {
		if pos[e.From] >= pos[e.To] {
			return fmt.Errorf("schedule violates %s -> %s", e.From, e.To)
		}
	}
	return nil
}

func (g *Graph) AddEdge(from, to string, e Evidence) {
	for i := range g.Edges {
		if g.Edges[i].From == from && g.Edges[i].To == to {
			for _, old := range g.Edges[i].Evidence {
				if old == e {
					return
				}
			}
			g.Edges[i].Evidence = append(g.Edges[i].Evidence, e)
			return
		}
	}
	g.Edges = append(g.Edges, Edge{From: from, To: to, Evidence: []Evidence{e}})
}

func (g *Graph) Canonicalize() {
	sort.Slice(g.Operations, func(i, j int) bool { return g.Operations[i].ID < g.Operations[j].ID })
	sort.Slice(g.Edges, func(i, j int) bool {
		a, b := g.Edges[i], g.Edges[j]
		if a.From == b.From {
			return a.To < b.To
		}
		return a.From < b.From
	})
	for i := range g.Edges {
		sort.Slice(g.Edges[i].Evidence, func(a, b int) bool { return Hash(g.Edges[i].Evidence[a]) < Hash(g.Edges[i].Evidence[b]) })
	}
	sort.Slice(g.Issues, func(i, j int) bool { return g.Issues[i].ID < g.Issues[j].ID })
}
