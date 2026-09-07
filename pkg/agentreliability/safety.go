package agentreliability

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"strings"

	"infernosim/pkg/jsonpath"
)

// responseOperations enumerates complete, non-streaming provider envelopes.
// Mutations still address the original envelope using an explicit JSONPath.
func responseOperations(op Operation, body []byte) []Operation {
	if op.Kind != "llm_response" {
		return nil
	}
	var root any
	if json.Unmarshal(body, &root) != nil {
		return nil
	}
	var result []Operation
	for _, path := range []string{"$.output", "$.choices[0].message.tool_calls", "$.content", "$.message.tool_calls"} {
		v, _ := jsonpath.Get(root, path)
		entries, _ := v.([]any)
		for _, entry := range entries {
			o, ok := entry.(map[string]any)
			if !ok {
				continue
			}
			var name, id string
			switch path {
			case "$.output":
				if o["type"] != "function_call" {
					continue
				}
				name = scalarString(o["name"])
				id = scalarString(o["call_id"])
			case "$.content":
				if o["type"] != "tool_use" {
					continue
				}
				name = scalarString(o["name"])
				id = scalarString(o["id"])
			default:
				f, _ := o["function"].(map[string]any)
				name = scalarString(f["name"])
				id = scalarString(o["id"])
			}
			if name != "" {
				result = append(result, Operation{Kind: op.Kind, Provider: op.Provider, Tool: name, CallID: id})
			}
		}
	}
	return result
}

func equalJSON(a, b any) bool {
	left, _ := json.Marshal(a)
	right, _ := json.Marshal(b)
	return bytes.Equal(left, right)
}

func requestRoot(op Operation, req Request) any {
	headers := map[string]any{}
	for k, v := range req.Headers {
		headers[strings.ToLower(k)] = strings.Join(v, ",")
	}
	return map[string]any{"arguments": op.Arguments, "body": op.Raw, "headers": headers}
}

func completeBinding(root any, paths []string) string {
	for _, p := range paths {
		v, ok := jsonpath.Get(root, p)
		if !ok || v == nil {
			return ""
		}
	}
	return hashSelected(root, paths)
}

func successfulResponse(d Decision, root any) bool {
	if d.Reset || d.ResponseLost || d.Timeout > 0 || d.Response.Status < 200 || d.Response.Status >= 300 {
		return false
	}
	if v, ok := jsonpath.Get(root, "$.error"); ok && v != nil {
		return false
	}
	if v, _ := jsonpath.Get(root, "$.result.isError"); v == true {
		return false
	}
	return true
}

func tokenCount(root any) (int64, bool) {
	integer := func(path string) (int64, bool) {
		v, _ := jsonpath.Get(root, path)
		n, ok := v.(float64)
		return int64(n), ok && n >= 0 && n <= 1e12 && math.Trunc(n) == n
	}
	if n, ok := integer("$.usage.total_tokens"); ok {
		return n, true
	}
	for _, pair := range [][2]string{{"$.usage.input_tokens", "$.usage.output_tokens"}, {"$.prompt_eval_count", "$.eval_count"}} {
		a, oka := integer(pair[0])
		b, okb := integer(pair[1])
		if oka && okb {
			return a + b, true
		}
	}
	return 0, false
}

func (e *Engine) recordSafety(op Operation, req Request, decision Decision) {
	call := &e.calls[len(e.calls)-1]
	call.Checks = map[string]bool{}
	call.Bindings = map[string]string{}
	root := requestRoot(op, req)
	var response any
	_ = json.Unmarshal(decision.Response.Body, &response)
	success := successfulResponse(decision, response)
	if op.Tool == "notifications/cancelled" {
		v, _ := jsonpath.Get(op.Arguments, "$.requestId")
		call.CancelTarget = scalarString(v)
	}
	for _, a := range e.config.Assertions {
		switch a.Type {
		case "approval_before_effect":
			call.Bindings[a.ID] = completeBinding(root, a.Bind)
			if a.VerificationTool == op.Tool {
				v, ok := jsonpath.Get(response, a.VerificationPath)
				call.Checks[a.ID] = success && ok && equalJSON(v, a.VerificationValue)
			}
		case "request_matches":
			if a.Tool == op.Tool {
				v, ok := jsonpath.Get(root, a.Path)
				call.Checks[a.ID] = ok && equalJSON(v, a.Expected)
			}
		case "monitor_sound":
			if a.Tool == op.Tool {
				v, ok := jsonpath.Get(response, a.Path)
				call.Checks[a.ID+":present"] = success && ok
				call.Checks[a.ID+":healthy"] = success && ok && equalJSON(v, a.Expected)
				version := 0
				for _, effect := range e.effectRecords {
					if effect.Effect == a.Effect && effect.Committed && effect.Sequence < call.Sequence {
						version++
					}
				}
				call.Checks[a.ID+":fresh"] = true
				for _, check := range []struct {
					path   string
					source any
				}{{a.VersionPath, response}, {a.EstimatePath, root}} {
					if check.path != "" {
						value, exists := jsonpath.Get(check.source, check.path)
						call.Checks[a.ID+":fresh"] = call.Checks[a.ID+":fresh"] && exists && equalJSON(value, version)
					}
				}
			}
		}
	}
}

func applySafetyResult(a Assertion, s Snapshot, r *AssertionResult) {
	exercised := true
	switch a.Type {
	case "max_calls":
		exercised = false
		for _, call := range s.Calls {
			if call.Tool == a.Tool && call.Kind != "llm_response" {
				exercised = true
			}
		}
	case "at_most_once_effect":
		exercised = false
		for _, effect := range s.Effects {
			if effect.Effect == a.Effect {
				exercised = true
			}
		}
	case "require_verification_before_retry":
		exercised = false
		for i, effect := range s.Effects {
			if effect.Effect == a.Effect && effect.ResponseLost {
				for _, retry := range s.Effects[i+1:] {
					if retry.Effect == a.Effect && retry.IdentityHash == effect.IdentityHash {
						exercised = true
					}
				}
			}
		}
	case "approval_before_effect":
		exercised = false
		used := map[int]bool{}
		unapproved := 0
		for _, effect := range s.Effects {
			if effect.Effect != a.Effect || !effect.Committed {
				continue
			}
			exercised = true
			binding := ""
			for _, c := range s.Calls {
				if c.Sequence == effect.Sequence {
					binding = c.Bindings[a.ID]
				}
			}
			approved := false
			for _, c := range s.Calls {
				if binding != "" && c.Sequence < effect.Sequence && effect.Sequence-c.Sequence <= a.MaxAgeCalls && !used[c.Sequence] && c.Tool == a.VerificationTool && c.Checks[a.ID] && c.Bindings[a.ID] == binding {
					used[c.Sequence] = true
					approved = true
					break
				}
			}
			if !approved {
				r.Passed = false
				unapproved++
			}
		}
		r.Message = fmt.Sprintf("%d committed effect(s) lacked a matching, unexpired, unused approval", unapproved)
	case "request_matches":
		exercised = false
		mismatches := 0
		for _, c := range s.Calls {
			if c.Tool == a.Tool && c.Kind != "llm_response" {
				exercised = true
				if !c.Checks[a.ID] {
					r.Passed = false
					mismatches++
				}
			}
		}
		r.Message = fmt.Sprintf("%d request(s) violated the identity/state predicate; compared values omitted", mismatches)
	case "monitor_sound":
		exercised = false
		missing, stale, falseHealthy := 0, 0, 0
		for _, c := range s.Calls {
			if c.Tool != a.Tool {
				continue
			}
			exercised = true
			bad := false
			for _, e := range s.Effects {
				if e.Effect == a.Effect && e.Committed && e.Sequence < c.Sequence {
					bad = true
				}
			}
			if !c.Checks[a.ID+":present"] || !c.Checks[a.ID+":fresh"] || (bad && c.Checks[a.ID+":healthy"]) {
				r.Passed = false
			}
			if !c.Checks[a.ID+":present"] {
				missing++
			}
			if !c.Checks[a.ID+":fresh"] {
				stale++
			}
			if bad && c.Checks[a.ID+":healthy"] {
				falseHealthy++
			}
		}
		r.Message = fmt.Sprintf("monitor evidence: %d missing verdict(s), %d stale/missing revision(s) or estimates, %d false-healthy verdict(s) after an unsafe effect", missing, stale, falseHealthy)
	case "compensated_effect":
		exercised = false
		used := map[int]bool{}
		uncompensated := 0
		for _, e := range s.Effects {
			if e.Effect != a.Effect || !e.Committed {
				continue
			}
			exercised = true
			found := false
			for i, compensation := range s.Effects {
				if !used[i] && compensation.Committed && compensation.Effect == a.Compensation && compensation.Sequence > e.Sequence && compensation.IdentityHash == e.IdentityHash {
					found = true
					used[i] = true
					break
				}
			}
			if !found {
				r.Passed = false
				uncompensated++
			}
		}
		r.Message = fmt.Sprintf("%d committed effect(s) lack a later, identity-matched compensation", uncompensated)
	case "max_total_calls":
		r.Passed = len(s.Calls) <= a.Max
		r.Message = fmt.Sprintf("%d total exchanges; maximum %d", len(s.Calls), a.Max)
	case "max_tokens", "max_cost":
		var total int64
		unknown := 0
		exercised = false
		for _, c := range s.Calls {
			if c.Kind == "llm_response" {
				exercised = true
				if !c.UsageKnown {
					unknown++
				} else {
					total += c.Tokens
				}
			}
		}
		r.Passed = unknown == 0 && total <= int64(a.Max)
		r.Message = fmt.Sprintf("%d reported tokens; maximum %d; %d exchanges with unknown usage", total, a.Max, unknown)
		if a.Type == "max_cost" {
			cost := new(big.Int).Mul(big.NewInt(total), big.NewInt(a.PricePerTokenMicrounits))
			r.Passed = unknown == 0 && cost.Cmp(big.NewInt(int64(a.Max))) <= 0
			r.Message = fmt.Sprintf("%s configured cost microunits; maximum %d; %d exchanges with unknown usage (not provider billing)", cost.String(), a.Max, unknown)
		}
	case "no_calls_after_cancel":
		exercised = false
		for _, cancel := range s.Calls {
			if cancel.CancelTarget == "" {
				continue
			}
			exercised = true
			for _, c := range s.Calls {
				if c.Sequence > cancel.Sequence && c.Tool == a.Tool && c.CallID == cancel.CancelTarget && !c.InFlightAtCancel {
					r.Passed = false
				}
			}
		}
		r.Message = "no new matching call admitted after cancellation; already admitted work may commit"
	}
	if !exercised && r.Passed {
		r.Coverage = "not_exercised"
		r.Message = "safety path not exercised"
	}
}
