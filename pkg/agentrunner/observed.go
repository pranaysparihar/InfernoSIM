package agentrunner

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"infernosim/pkg/causal"
	"infernosim/pkg/mcpproxy"
	"infernosim/pkg/replaydriver"
	"infernosim/pkg/stubproxy"
	"infernosim/pkg/traceorder"
)

type ObservedBoundary struct {
	Node     string            `json:"node"`
	Stamp    *traceorder.Stamp `json:"stamp,omitempty"`
	Source   string            `json:"source"`
	Digest   string            `json:"digest"`
	Terminal bool              `json:"cancellation_terminal,omitempty"`
}

// ObservedCapture never adds chronology as a causal dependency. Partial evidence
// stays inspectable but cannot be used as an exact observed replay schedule.
type ObservedCapture struct {
	EvidenceComplete bool               `json:"evidence_complete"`
	Version          int                `json:"version"`
	Status           string             `json:"status"`
	Graph            causal.Graph       `json:"graph"`
	Schedule         *causal.Schedule   `json:"schedule,omitempty"`
	Boundaries       []ObservedBoundary `json:"boundaries"`
	Issues           []string           `json:"issues,omitempty"`
}

func ConvertObserved(incident string, d causal.Declarations) (ObservedCapture, error) {
	r := ObservedCapture{Version: 1, Status: "partial"}
	g, err := DiscoverCausal(incident, d)
	if err != nil {
		return r, err
	}
	r.Graph = g
	graphErr := g.Validate(true)
	hasHTTPObservations := false
	b, err := replaydriver.OpenBundle(incident)
	if err != nil {
		return r, err
	}
	allStamps := map[string]map[uint64]bool{}
	observe := func(s *traceorder.Stamp) {
		if s == nil {
			return
		}
		if allStamps[s.Session] == nil {
			allStamps[s.Session] = map[uint64]bool{}
		}
		if allStamps[s.Session][s.Sequence] {
			r.Issues = append(r.Issues, "duplicate raw observation counter")
		}
		allStamps[s.Session][s.Sequence] = true
	}
	add := func(node string, stamp *traceorder.Stamp, source, digest string, terminal bool) {
		r.Boundaries = append(r.Boundaries, ObservedBoundary{node, stamp, source, digest, terminal})
	}
	if b.HasOutbound() {
		events, err := stubproxy.LoadOutboundEvents(b.OutboundLog)
		if err != nil {
			return r, err
		}
		hasHTTPObservations = len(events) > 0
		for _, e := range events {
			observe(e.CaptureAdmit)
			observe(e.CaptureRelease)
			add(causal.Admit(e.ID), e.CaptureAdmit, "event:"+e.ID, causal.Hash(e), false)
			add(causal.Release(e.ID), e.CaptureRelease, "event:"+e.ID, causal.Hash(e), false)
		}
	}
	if b.HasMCP() {
		records, err := mcpproxy.Load(b.MCPLog)
		if err != nil {
			return r, err
		}
		requests := map[string]string{}
		completed := map[string]bool{}
		cancels := map[string]mcpproxy.Record{}
		for _, v := range records {
			observe(v.Received)
			observe(v.ForwardStart)
			x, err := causal.Decode(v.Message)
			if err != nil {
				return r, err
			}
			m := x.(map[string]any)
			method, _ := m["method"].(string)
			id, hasID := m["id"]
			key := causal.Hash(id)
			op := fmt.Sprintf("mcp_%d", v.Sequence)
			source := fmt.Sprintf("mcp:%d", v.Sequence)
			if method != "" {
				add(causal.Admit(op), v.Received, source, causal.Hash(v), false)
				if hasID {
					requests[key] = op
				} else {
					add(causal.Release(op), v.ForwardStart, source, causal.Hash(v), false)
				}
				if method == "notifications/cancelled" {
					p, _ := m["params"].(map[string]any)
					cancels[causal.Hash(p["requestId"])] = v
				}
			} else {
				if requests[key] == "" {
					return r, fmt.Errorf("response precedes its captured request")
				}
				add(causal.Release(requests[key]), v.ForwardStart, source, causal.Hash(v), false)
				completed[key] = true
			}
		}
		for key, op := range requests {
			if !completed[key] {
				v, ok := cancels[key]
				if !ok {
					return r, fmt.Errorf("missing response/terminal cancellation")
				}
				add(causal.Release(op), v.ForwardStart, fmt.Sprintf("mcp:%d", v.Sequence), causal.Hash(v), true)
			}
		}
	}
	sessions := map[string]bool{}
	seen := map[uint64]ObservedBoundary{}
	for _, b := range r.Boundaries {
		if b.Stamp == nil || b.Stamp.Session == "" || b.Stamp.Sequence == 0 {
			r.Issues = append(r.Issues, "missing precise boundary evidence: "+b.Node)
			continue
		}
		sessions[b.Stamp.Session] = true
		if prior, ok := seen[b.Stamp.Sequence]; ok && prior.Stamp.Session == b.Stamp.Session && !(b.Terminal && prior.Source == b.Source) {
			r.Issues = append(r.Issues, "ambiguous duplicate observation counter: "+b.Node)
		}
		seen[b.Stamp.Sequence] = b
	}
	if len(sessions) > 1 {
		r.Issues = append(r.Issues, "unrelated observation clocks: mixed order is unknown; wall-clock timestamps cannot resolve it")
	}
	var seal traceorder.Stamp
	data, sealErr := readRegular(filepath.Join(incident, "capture-session.json"), 1024)
	if os.IsNotExist(sealErr) && b.HasMCP() && !hasHTTPObservations {
		data, sealErr = readRegular(b.MCPLog+".capture-session.json", 1024)
	}
	if sealErr == nil {
		if err := decodeStrict(data, &seal); err != nil {
			return r, err
		}
		if seal.Sequence == 0 || seal.Sequence > 1000000 || len(allStamps) != 1 || len(allStamps[seal.Session]) != int(seal.Sequence) {
			r.Issues = append(r.Issues, "capture session has missing or inconsistent observation evidence")
		}
		for n := uint64(1); n <= seal.Sequence && n <= 1000000; n++ {
			if !allStamps[seal.Session][n] {
				r.Issues = append(r.Issues, "capture session observation counter gap")
				break
			}
		}
	} else if !os.IsNotExist(sealErr) {
		return r, sealErr
	} else {
		r.Issues = append(r.Issues, "capture is not sealed; use agent causal record or retain the MCP capture-session sidecar")
	}
	sort.Slice(r.Boundaries, func(i, j int) bool {
		a, b := r.Boundaries[i], r.Boundaries[j]
		if a.Stamp == nil || b.Stamp == nil {
			return a.Stamp != nil || (b.Stamp == nil && a.Node < b.Node)
		}
		if a.Stamp.Session != b.Stamp.Session {
			return a.Stamp.Session < b.Stamp.Session
		}
		if a.Stamp.Sequence != b.Stamp.Sequence {
			return a.Stamp.Sequence < b.Stamp.Sequence
		}
		if a.Terminal != b.Terminal {
			return !a.Terminal
		}
		return a.Node < b.Node
	})
	r.EvidenceComplete = len(r.Issues) == 0
	if graphErr != nil {
		r.Issues = append(r.Issues, graphErr.Error())
	}
	sort.Strings(r.Issues)
	if len(r.Issues) > 0 {
		return r, nil
	}
	order := make([]string, len(r.Boundaries))
	for i, b := range r.Boundaries {
		order[i] = b.Node
	}
	if err = g.ValidateOrder(order); err != nil {
		return r, fmt.Errorf("observed order conflicts with reviewed graph: %w", err)
	}
	s := causal.NewSchedule(g, order)
	r.Schedule = &s
	r.Status = "exact-proxy-observation"
	return r, nil
}

func ReadObserved(path, incident string) (ObservedCapture, error) {
	var r ObservedCapture
	data, err := readRegular(path, 16<<20)
	if err != nil {
		return r, err
	}
	if err = decodeStrict(data, &r); err != nil {
		return r, err
	}
	if r.Version != 1 || r.Status != "exact-proxy-observation" || r.Schedule == nil || len(r.Issues) > 0 {
		return r, fmt.Errorf("capture has no exact observed schedule")
	}
	if err = r.Schedule.Validate(r.Graph); err != nil {
		return r, err
	}
	if err = ValidateCausalRecording(incident, r.Graph); err != nil {
		return r, err
	}
	// Reconstruct evidence using the supplied reviewed graph, then compare the
	// boundaries independently. A modified order with a fresh hash is not proof.
	current, err := ConvertObserved(incident, causal.Declarations{})
	if err != nil {
		return r, err
	}
	if !current.EvidenceComplete {
		return r, fmt.Errorf("recording has incomplete or ambiguous boundary evidence")
	}
	if causal.Hash(current.Boundaries) != causal.Hash(r.Boundaries) {
		return r, fmt.Errorf("capture boundary evidence changed")
	}
	if len(r.Boundaries) != len(r.Schedule.Order) {
		return r, fmt.Errorf("capture boundary count mismatch")
	}
	session := ""
	var sequence uint64
	for i, b := range r.Boundaries {
		if b.Stamp == nil || b.Stamp.Sequence == 0 || b.Stamp.Session == "" {
			return r, fmt.Errorf("missing observed boundary stamp")
		}
		if session == "" {
			session = b.Stamp.Session
		}
		if session != b.Stamp.Session || b.Stamp.Sequence < sequence || (b.Stamp.Sequence == sequence && (!b.Terminal || i == 0 || r.Boundaries[i-1].Source != b.Source)) {
			return r, fmt.Errorf("ambiguous observed boundary order")
		}
		sequence = b.Stamp.Sequence
		if r.Schedule.Order[i] != b.Node {
			return r, fmt.Errorf("schedule differs from observed boundary order")
		}
	}
	return r, nil
}
