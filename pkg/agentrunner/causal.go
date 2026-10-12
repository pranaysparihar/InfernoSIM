package agentrunner

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"infernosim/pkg/agentotel"
	"infernosim/pkg/agentreliability"
	"infernosim/pkg/causal"
	"infernosim/pkg/mcpproxy"
	"infernosim/pkg/replaydriver"
	"infernosim/pkg/stubproxy"
)

func DiscoverCausal(incident string, declarations causal.Declarations) (causal.Graph, error) {
	b, err := replaydriver.OpenBundle(incident)
	if err != nil {
		return causal.Graph{}, err
	}
	var xs []causal.Exchange
	if b.HasOutbound() {
		events, err := stubproxy.LoadOutboundEvents(b.OutboundLog)
		if err != nil {
			return causal.Graph{}, err
		}
		for _, e := range events {
			x, err := causal.HTTPExchange(e)
			if err != nil {
				return causal.Graph{}, err
			}
			xs = append(xs, x)
		}
	}
	if b.HasMCP() {
		records, err := mcpproxy.Load(b.MCPLog)
		if err != nil {
			return causal.Graph{}, err
		}
		mcp, err := mcpproxy.Exchanges(records)
		if err != nil {
			return causal.Graph{}, err
		}
		xs = append(xs, mcp...)
	}
	var spans []agentotel.Span
	if b.HasAgentSpans() {
		data, err := readRegular(b.AgentSpans, agentotel.MaximumInputSize)
		if err != nil {
			return causal.Graph{}, err
		}
		spans, err = agentotel.Parse(data)
		if err != nil {
			return causal.Graph{}, err
		}
	}
	causal.SortedExchanges(xs)
	return causal.Discover(xs, spans, declarations)
}

func ValidateCausalRecording(incident string, g causal.Graph) error {
	if err := g.Validate(true); err != nil {
		return err
	}
	current, err := DiscoverCausal(incident, causal.Declarations{})
	if err != nil {
		return err
	}
	if len(current.Operations) != len(g.Operations) {
		return fmt.Errorf("causal graph operation count differs from recording")
	}
	byRecord := map[string]causal.Operation{}
	digests := map[string]string{}
	for _, o := range current.Operations {
		byRecord[o.Record] = o
	}
	for _, e := range current.Edges {
		for _, v := range e.Evidence {
			if v.Rule == "request_before_response" {
				digests[v.Source] = v.SHA256
			}
		}
	}
	for _, o := range g.Operations {
		actual, ok := byRecord[o.Record]
		if !ok || actual.Fingerprint != o.Fingerprint || actual.RequestID != o.RequestID || actual.Notification != o.Notification || actual.ServerNotification != o.ServerNotification {
			return fmt.Errorf("causal operation %s is incompatible with recording", o.ID)
		}
		delete(byRecord, o.Record)
		verified := false
		for _, e := range g.Edges {
			if e.From == causal.Admit(o.ID) && e.To == causal.Release(o.ID) {
				for _, v := range e.Evidence {
					verified = verified || (v.Rule == "request_before_response" && v.Source == o.Record && v.SHA256 == digests[o.Record])
				}
			}
		}
		if !verified {
			return fmt.Errorf("causal recording evidence changed for %s", o.ID)
		}
	}
	spanDigests := map[string]string{}
	spanRecording := ""
	if data, err := readRegular(filepath.Join(incident, "agent-spans.jsonl"), agentotel.MaximumInputSize); err == nil {
		spans, err := agentotel.Parse(data)
		if err != nil {
			return err
		}
		for _, s := range spans {
			spanDigests["span:"+s.TraceID+"/"+s.SpanID] = causal.Hash(s)
		}
		if len(spans) > 0 {
			spanRecording = causal.SpanDigest(spans)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	spanEvidence := false
	for _, e := range g.Edges {
		for _, v := range e.Evidence {
			if v.Rule == "otel_recording" {
				if v.Source != "agent-spans" || v.SHA256 != spanRecording {
					return fmt.Errorf("causal OpenTelemetry recording evidence changed")
				}
				spanEvidence = true
			}
			if strings.HasPrefix(v.Source, "span:") && spanDigests[v.Source] != v.SHA256 {
				return fmt.Errorf("causal span evidence changed: %s", v.Source)
			}
		}
	}
	if spanRecording != "" && !spanEvidence {
		return fmt.Errorf("causal graph is missing OpenTelemetry recording evidence")
	}
	return nil
}

func planCausalExploration(config agentreliability.Config, scope string, o ExploreOptions) (ExplorationPlan, error) {
	p := ExplorationPlan{Version: 2, Options: o}
	if o.PermuteSchedule != "" || len(config.Schedules) > 0 {
		return p, fmt.Errorf("causal exploration cannot combine admission-only schedules")
	}
	if o.ScheduleBudget == 0 {
		o.ScheduleBudget = 16
	}
	if o.BoundaryTimeout == "" {
		o.BoundaryTimeout = "10s"
	}
	p.Options = o
	if o.Budget < 1 || o.Budget > 1000 {
		return p, fmt.Errorf("case budget must be 1..1000")
	}
	schedules, err := causal.Generate(*o.CausalGraph, o.Seed, o.ScheduleBudget)
	if err != nil {
		return p, err
	}
	p.CausalPlan = &schedules
	baseOptions := o
	baseOptions.CausalGraph = nil
	baseOptions.Budget = 1000
	base, err := PlanExploration(config, scope, baseOptions)
	if err != nil {
		return p, err
	}
	for _, trial := range base.Trials {
		for _, schedule := range schedules.Schedules {
			if len(p.Trials) == o.Budget {
				p.Truncated = true
				return p, nil
			}
			t := copyTrial(trial)
			t.Causal = &causal.Replay{Graph: *o.CausalGraph, Schedule: schedule, Timeout: o.BoundaryTimeout}
			t.Case.ScheduleID = schedule.ID
			t.Case.Description = "causal admission and response-release exploration"
			if _, err := t.AgentConfig(config); err != nil {
				return p, err
			}
			p.Trials = append(p.Trials, identifyTrial(t, scope, o.Seed))
		}
	}
	p.Truncated = base.Truncated || schedules.Truncated
	return p, nil
}

// ReadGraph is bounded and strict; a graph is data and can never supply a
// command to execute on the user's machine.
func ReadGraph(path string) (causal.Graph, error) {
	var g causal.Graph
	data, err := readRegular(path, 16<<20)
	if err != nil {
		return g, err
	}
	if err = decodeStrict(data, &g); err != nil {
		return g, err
	}
	return g, g.Validate(true)
}

func decodeStrict(data []byte, v any) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return err
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return fmt.Errorf("expected one JSON document")
	}
	return nil
}

func HasCausalMCP(config agentreliability.Config) bool {
	if config.Causal == nil {
		return false
	}
	for _, o := range config.Causal.Graph.Operations {
		if o.Protocol == "mcp" {
			return true
		}
	}
	return false
}

// ExecutablePath is supplied by the CLI when embedding the stdio connector.
// Library callers may set Options.MCPConnector explicitly for another binary.
func executablePath() string { p, _ := os.Executable(); return p }
