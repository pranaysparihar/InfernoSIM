package toolstream

import (
	"fmt"
	"strconv"
)

// ParseEnvelope exposes the same per-tool target for complete JSON responses.
// Existing whole-envelope JSONPath mutations remain a separate API.
func ParseEnvelope(provider string, data []byte) (*Stream, error) {
	if len(data) > MaximumBytes {
		return nil, fmt.Errorf("provider envelope exceeds size limit")
	}
	v, err := decode(data)
	if err != nil {
		return nil, err
	}
	root := object(v)
	if root == nil {
		return nil, fmt.Errorf("provider envelope must be an object")
	}
	s := &Stream{frames: []frame{{raw: append([]byte(nil), data...), payload: []span{{0, len(data)}}, value: root}}, provider: provider, finished: true}
	add := func(id, name string, m map[string]any, field string, asObject bool) error {
		key := strconv.Itoa(len(s.Calls))
		if id == "" && provider == "ollama" {
			id = "ollama:" + key
		}
		call, err := s.add(key, id, name)
		if err != nil {
			return err
		}
		if err = call.complete(0, m, field, asObject); err != nil {
			return err
		}
		call.done = true
		return nil
	}
	switch provider {
	case "openai":
		for _, v := range array(root["output"]) {
			m := object(v)
			if m["type"] == "function_call" {
				if err := add(text(m["call_id"]), text(m["name"]), m, "arguments", false); err != nil {
					return nil, err
				}
			}
		}
		for _, choice := range array(root["choices"]) {
			for _, v := range array(object(object(choice)["message"])["tool_calls"]) {
				m := object(v)
				f := object(m["function"])
				if err := add(text(m["id"]), text(f["name"]), f, "arguments", false); err != nil {
					return nil, err
				}
			}
		}
	case "anthropic":
		for _, v := range array(root["content"]) {
			m := object(v)
			if m["type"] == "tool_use" {
				if err := add(text(m["id"]), text(m["name"]), m, "input", true); err != nil {
					return nil, err
				}
			}
		}
	case "ollama":
		for _, v := range array(object(root["message"])["tool_calls"]) {
			m := object(v)
			f := object(m["function"])
			if err := add(text(m["id"]), text(f["name"]), f, "arguments", true); err != nil {
				return nil, err
			}
		}
	default:
		return nil, fmt.Errorf("unsupported provider argument target %q", provider)
	}
	if len(s.Calls) == 0 {
		return nil, fmt.Errorf("provider response has no supported tool calls")
	}
	return s, nil
}
