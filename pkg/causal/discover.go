package causal

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"infernosim/pkg/agentotel"
	"infernosim/pkg/toolstream"
)

// Exchange is a complete captured boundary. Bodies are used for correlation but
// only digests and identities are retained in the graph.
type Exchange struct {
	// RecordingHash binds transport metadata (status, headers, chunks and timing)
	// when the source format contains more than the request/response bodies.
	RecordingHash                                                       string
	ResponseContentType                                                 string
	ID, Protocol, Method, Host, Path, Record, Identity, TraceID, SpanID string
	Request, Response                                                   []byte
	Notification                                                        bool
	ServerNotification                                                  bool
}

type Declaration struct {
	From   string `json:"from"`
	To     string `json:"to"`
	Reason string `json:"reason"`
}
type Declarations struct {
	Review     string            `json:"review"`
	Edges      []Declaration     `json:"edges,omitempty"`
	Identities map[string]string `json:"identities,omitempty"`
	// Resolutions name an issue and explain the evidence reviewed by a human.
	Resolutions    map[string]string `json:"resolutions,omitempty"`
	SpanOperations map[string]string `json:"span_operations,omitempty"`
}

type tool struct{ id, name, args string }

func str(v any) string         { s, _ := v.(string); return s }
func obj(v any) map[string]any { m, _ := v.(map[string]any); return m }
func arr(v any) []any          { a, _ := v.([]any); return a }
func argumentHash(v any) string {
	if s, ok := v.(string); ok {
		if value, err := Decode([]byte(s)); err == nil {
			return Hash(value)
		}
	}
	return Hash(v)
}

func proposals(body []byte) []tool {
	v, err := Decode(body)
	if err != nil {
		return nil
	}
	root := obj(v)
	var result []tool
	for _, value := range arr(root["output"]) {
		m := obj(value)
		if m["type"] == "function_call" {
			result = append(result, tool{str(m["call_id"]), str(m["name"]), argumentHash(m["arguments"])})
		}
	}
	for _, value := range arr(root["content"]) {
		m := obj(value)
		if m["type"] == "tool_use" {
			result = append(result, tool{str(m["id"]), str(m["name"]), argumentHash(m["input"])})
		}
	}
	messages := []any{root["message"]}
	for _, choice := range arr(root["choices"]) {
		messages = append(messages, obj(choice)["message"])
	}
	for _, message := range messages {
		for _, value := range arr(obj(message)["tool_calls"]) {
			m := obj(value)
			f := obj(m["function"])
			result = append(result, tool{str(m["id"]), str(f["name"]), argumentHash(f["arguments"])})
		}
	}
	return result
}

func consumed(body []byte) []string {
	v, err := Decode(body)
	if err != nil {
		return nil
	}
	root := obj(v)
	var result []string
	var walk func(any)
	walk = func(value any) {
		switch x := value.(type) {
		case []any:
			for _, item := range x {
				walk(item)
			}
		case map[string]any:
			if x["type"] == "function_call_output" {
				result = append(result, str(x["call_id"]))
			}
			if x["type"] == "tool_result" {
				result = append(result, str(x["tool_use_id"]))
			}
			if x["role"] == "tool" {
				result = append(result, str(x["tool_call_id"]))
			}
			if content, ok := x["content"]; ok {
				walk(content)
			}
		}
	}
	walk(root["input"])
	walk(root["messages"])
	return result
}

// Discover deliberately ignores wall-clock ordering. Parent spans establish
// start causality, while explicit model tool proposals and consumed results
// establish release-to-admission dependencies. OTel links remain review issues.
func Discover(exchanges []Exchange, spans []agentotel.Span, d Declarations) (Graph, error) {
	g := Graph{Version: Version, Review: d.Review}
	issue := func(kind, detail string, blocking bool) {
		id := kind + "_" + Digest([]byte(detail))[:12]
		g.Issues = append(g.Issues, Issue{ID: id, Kind: kind, Detail: detail, Blocking: blocking, Resolution: d.Resolutions[id]})
	}
	if len(exchanges) == 0 || len(exchanges) > MaximumOperations {
		return g, fmt.Errorf("discovery requires 1..%d complete exchanges", MaximumOperations)
	}
	byID := map[string]Exchange{}
	requests := map[string][]string{}
	rpcIDs := map[string][]string{}
	responseIDs := map[string][]string{}
	for _, x := range exchanges {
		if _, exists := byID[x.ID]; exists {
			return g, fmt.Errorf("duplicate exchange ID %s", x.ID)
		}
		byID[x.ID] = x
		fp, identity, rpcID := RequestFingerprint(x.Protocol, x.Method, x.Host, x.Path, x.Request)
		if x.Identity != "" {
			identity = x.Identity
		}
		if declared := d.Identities[x.ID]; declared != "" {
			identity = declared
		}
		g.Operations = append(g.Operations, Operation{ID: x.ID, Protocol: x.Protocol, Fingerprint: fp, Record: x.Record, RequestID: rpcID, Identity: identity, Notification: x.Notification, ServerNotification: x.ServerNotification})
		g.AddEdge(Admit(x.ID), Release(x.ID), Evidence{Rule: "request_before_response", Source: x.Record, SHA256: exchangeDigest(x)})
		v, _ := Decode(x.Request)
		root := obj(v)
		if root["method"] == "tools/call" {
			p := obj(root["params"])
			key := str(p["name"]) + ":" + argumentHash(p["arguments"])
			requests[key] = append(requests[key], x.ID)
		}
		if rpcID != "" {
			rpcIDs[rpcID] = append(rpcIDs[rpcID], x.ID)
		}
		v, _ = Decode(x.Response)
		if id := str(obj(v)["id"]); id != "" && obj(v)["object"] == "response" {
			responseIDs[id] = append(responseIDs[id], x.ID)
		}
	}
	for id := range d.Identities {
		if _, ok := byID[id]; !ok {
			return g, fmt.Errorf("identity declaration references unknown operation %s", id)
		}
	}
	toolOperations := map[string][]string{}
	for _, x := range exchanges {
		calls := proposals(x.Response)
		if toolstream.IsStream(x.ResponseContentType) {
			stream, err := toolstream.DetectAndParse(x.Response)
			if err != nil {
				issue("unsupported_stream", "recorded provider stream "+x.ID+": "+err.Error(), true)
			} else {
				if stream.ResponseID != "" {
					responseIDs[stream.ResponseID] = append(responseIDs[stream.ResponseID], x.ID)
				}
				for _, call := range stream.Calls {
					calls = append(calls, tool{call.ID, call.Name, argumentHash(string(call.Arguments))})
				}
			}
		}
		for _, call := range calls {
			candidates := requests[call.name+":"+call.args]
			if len(candidates) != 1 {
				issue("tool_correlation", fmt.Sprintf("proposal %s in %s matches %d recorded tool requests", call.id, x.ID, len(candidates)), true)
				continue
			}
			target := candidates[0]
			if target == x.ID {
				continue
			}
			g.AddEdge(Release(x.ID), Admit(target), Evidence{Rule: "model_tool_proposal", Source: x.Record, SHA256: Digest(x.Response), Detail: "tool identity and canonical argument digest"})
			if call.id != "" {
				toolOperations[call.id] = append(toolOperations[call.id], target)
			} else {
				issue("missing_tool_identity", "tool proposal in "+x.ID+" has no call identity for result correlation", false)
			}
		}
	}
	for _, x := range exchanges {
		for _, id := range consumed(x.Request) {
			candidates := toolOperations[id]
			if id == "" || len(candidates) != 1 {
				issue("result_correlation", fmt.Sprintf("consumed result %s in %s matches %d tool operations", id, x.ID, len(candidates)), true)
				continue
			}
			g.AddEdge(Release(candidates[0]), Admit(x.ID), Evidence{Rule: "consumed_tool_result", Source: x.Record, SHA256: Digest(x.Request), Detail: "provider tool result references proposal identity"})
		}
		v, _ := Decode(x.Request)
		root := obj(v)
		if previous := str(root["previous_response_id"]); previous != "" {
			candidates := responseIDs[previous]
			if len(candidates) != 1 {
				issue("missing_response", "previous response for "+x.ID+" is missing or ambiguous", true)
			} else {
				g.AddEdge(Release(candidates[0]), Admit(x.ID), Evidence{Rule: "previous_response_id", Source: x.Record, SHA256: Digest(x.Request)})
			}
		}
		if root["method"] == "notifications/cancelled" {
			id, _ := json.Marshal(obj(root["params"])["requestId"])
			candidates := rpcIDs[string(id)]
			if len(candidates) != 1 {
				issue("cancel_target", "cancellation "+x.ID+" has a missing or ambiguous target", true)
			} else {
				g.AddEdge(Admit(candidates[0]), Admit(x.ID), Evidence{Rule: "cancellation_target", Source: x.Record, SHA256: Digest(x.Request)})
				if len(byID[candidates[0]].Response) == 0 {
					g.AddEdge(Release(x.ID), Release(candidates[0]), Evidence{Rule: "cancelled_without_response", Source: x.Record, SHA256: Digest(x.Request)})
				}
			}
		}
	}
	// MCP initialization is a protocol dependency, not a timestamp heuristic.
	var initializations, initialized []string
	for _, x := range exchanges {
		v, _ := Decode(x.Request)
		switch obj(v)["method"] {
		case "initialize":
			initializations = append(initializations, x.ID)
		case "notifications/initialized":
			initialized = append(initialized, x.ID)
		}
	}
	if len(initializations) > 0 || len(initialized) > 0 {
		if len(initializations) != 1 || len(initialized) != 1 {
			issue("mcp_initialization", "recording must contain one initialize exchange and one initialized notification", true)
		} else {
			x := byID[initialized[0]]
			e := Evidence{Rule: "mcp_initialization", Source: x.Record, SHA256: Digest(x.Request)}
			g.AddEdge(Release(initializations[0]), Admit(initialized[0]), e)
			for _, x := range exchanges {
				v, _ := Decode(x.Request)
				method := str(obj(v)["method"])
				if method != "" && x.ID != initializations[0] && x.ID != initialized[0] {
					g.AddEdge(Release(initialized[0]), Admit(x.ID), e)
				}
			}
		}
	}
	spanMap := map[string]string{}
	for _, x := range exchanges {
		if !x.ServerNotification {
			continue
		}
		v, _ := Decode(x.Request)
		m := obj(v)
		if m["method"] == "notifications/progress" {
			token := obj(m["params"])["progressToken"]
			var matches []string
			for _, request := range exchanges {
				if request.ServerNotification {
					continue
				}
				v, _ := Decode(request.Request)
				meta := obj(obj(obj(v)["params"])["_meta"])
				if token != nil && Hash(meta["progressToken"]) == Hash(token) {
					matches = append(matches, request.ID)
				}
			}
			if len(matches) != 1 {
				issue("progress_correlation", "progress notification "+x.ID+" needs one matching request token", true)
			} else {
				e := Evidence{Rule: "mcp_progress", Source: x.Record, SHA256: Digest(x.Request)}
				g.AddEdge(Admit(matches[0]), Admit(x.ID), e)
				g.AddEdge(Release(x.ID), Release(matches[0]), e)
			}
		}
	}
	spanByID := map[string]agentotel.Span{}
	if len(spans) > 0 {
		first := exchanges[0].ID
		for _, x := range exchanges {
			if x.ID < first {
				first = x.ID
			}
		}
		g.AddEdge(Admit(first), Release(first), Evidence{Rule: "otel_recording", Source: "agent-spans", SHA256: SpanDigest(spans)})
	}
	for _, s := range spans {
		key := s.TraceID + "/" + s.SpanID
		if _, ok := spanByID[key]; ok {
			return g, fmt.Errorf("duplicate span %s", key)
		}
		spanByID[key] = s
		var matches []string
		if id := d.SpanOperations[key]; id != "" {
			if _, ok := byID[id]; !ok {
				return g, fmt.Errorf("span declaration references unknown operation %s", id)
			}
			matches = append(matches, id)
		} else {
			for _, x := range exchanges {
				if x.TraceID == s.TraceID && x.SpanID == s.SpanID {
					matches = append(matches, x.ID)
				} else if s.ToolCallID != "" {
					for _, id := range toolOperations[s.ToolCallID] {
						if id == x.ID {
							matches = append(matches, id)
						}
					}
				}
			}
		}
		if len(matches) == 1 {
			spanMap[key] = matches[0]
		} else if len(matches) > 1 {
			issue("span_correlation", "span "+key+" matches multiple operations", true)
		}
		if len(s.Links) > 0 {
			issue("span_links", "span "+key+" links require explicit ordering declarations; links do not identify start/finish boundaries", true)
		}
	}
	for key, id := range spanMap {
		s := spanByID[key]
		visited := map[string]bool{key: true}
		parent := s.ParentSpanID
		for parent != "" {
			pk := s.TraceID + "/" + parent
			if visited[pk] {
				issue("span_cycle", "parent cycle at "+pk, true)
				break
			}
			visited[pk] = true
			p, ok := spanByID[pk]
			if !ok {
				issue("missing_parent", "missing parent span "+pk, true)
				break
			}
			if parentID := spanMap[pk]; parentID != "" && parentID != id {
				g.AddEdge(Admit(parentID), Admit(id), Evidence{Rule: "otel_parent_start", Source: "span:" + key, SHA256: Hash(s)})
				break
			}
			parent = p.ParentSpanID
		}
	}
	for key := range d.SpanOperations {
		if _, ok := spanByID[key]; !ok {
			return g, fmt.Errorf("span declaration references unknown span %s", key)
		}
	}
	for _, decl := range d.Edges {
		if strings.TrimSpace(decl.Reason) == "" {
			return g, fmt.Errorf("declared edges require a reason")
		}
		g.AddEdge(decl.From, decl.To, Evidence{Rule: "user_declaration", Source: "declarations", SHA256: Hash(decl), Detail: decl.Reason})
	}
	issue("unobserved_dependencies", "recordings cannot establish the absence of hidden application dependencies; review before scheduling", false)
	// Canonical issue order also removes duplicate references to the same gap.
	g.Canonicalize()
	if len(g.Issues) > 0 {
		unique := g.Issues[:1]
		for _, i := range g.Issues[1:] {
			if i.ID != unique[len(unique)-1].ID {
				unique = append(unique, i)
			}
		}
		g.Issues = unique
	}
	for id := range d.Resolutions {
		found := false
		for _, i := range g.Issues {
			found = found || i.ID == id
		}
		if !found {
			return g, fmt.Errorf("resolution references unknown issue %s", id)
		}
	}
	if err := g.Validate(false); err != nil {
		return g, err
	}
	return g, nil
}

// SortedExchanges is useful when input records come from unordered stores.
func SortedExchanges(x []Exchange) { sort.Slice(x, func(i, j int) bool { return x[i].ID < x[j].ID }) }

// SpanDigest includes generic ancestors as well as mapped spans. Changing an
// otherwise-unmapped parent's ancestry must invalidate retained edge evidence.
func SpanDigest(spans []agentotel.Span) string {
	copy := append([]agentotel.Span(nil), spans...)
	sort.Slice(copy, func(i, j int) bool { return copy[i].TraceID+"/"+copy[i].SpanID < copy[j].TraceID+"/"+copy[j].SpanID })
	return Hash(copy)
}
