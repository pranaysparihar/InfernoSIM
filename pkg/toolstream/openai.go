package toolstream

import (
	"fmt"
	"strings"
)

func (s *Stream) openAI() error {
	family := ""
	for _, f := range s.frames {
		if f.value == nil {
			continue
		}
		if f.value["object"] == "chat.completion.chunk" {
			family = "chat"
		} else if strings.HasPrefix(text(f.value["type"]), "response.") {
			family = "responses"
		}
		break
	}
	if family == "chat" {
		return s.chat()
	}
	if family == "responses" {
		return s.responses()
	}
	return fmt.Errorf("unsupported OpenAI stream event family")
}

func (s *Stream) chat() error {
	choices := map[string]bool{}
	finished := map[string]bool{}
	for i, f := range s.frames {
		if f.value == nil && !f.done {
			continue
		}
		if s.finished {
			return fmt.Errorf("OpenAI Chat event after terminal completion")
		}
		if f.done {
			for choice := range choices {
				if !finished[choice] {
					return fmt.Errorf("OpenAI Chat choice lacks finish_reason")
				}
			}
			if len(choices) == 0 {
				return fmt.Errorf("OpenAI Chat stream contains no choices")
			}
			s.finished = true
			continue
		}
		m := f.value
		if m["object"] != "chat.completion.chunk" {
			return fmt.Errorf("unsupported OpenAI Chat stream event")
		}
		if usage := object(m["usage"]); usage != nil {
			s.Usage = map[string]any{"usage": usage}
		}
		for _, value := range array(m["choices"]) {
			choice := object(value)
			ci, err := index(choice["index"])
			if err != nil {
				return err
			}
			choices[ci] = true
			delta := object(choice["delta"])
			if finished[ci] {
				return fmt.Errorf("OpenAI Chat choice continued after finish_reason")
			}
			for _, v := range array(delta["tool_calls"]) {
				entry := object(v)
				ti, err := index(entry["index"])
				if err != nil {
					return err
				}
				key := ci + "/" + ti
				function := object(entry["function"])
				call, err := s.call(key)
				if err != nil {
					call, err = s.add(key, text(entry["id"]), text(function["name"]))
					if err != nil {
						return err
					}
				} else {
					if id := text(entry["id"]); id != "" && id != call.ID {
						return fmt.Errorf("OpenAI Chat tool identity changed")
					}
					if name := text(function["name"]); name != "" && name != call.Name {
						return fmt.Errorf("unsupported fragmented or conflicting tool name")
					}
				}
				if _, exists := function["arguments"]; exists {
					if err := call.fragment(i, function, "arguments"); err != nil {
						return err
					}
				}
			}
			if reason := text(choice["finish_reason"]); reason != "" {
				if reason != "stop" && reason != "tool_calls" {
					return fmt.Errorf("OpenAI Chat stream ended with %s", reason)
				}
				finished[ci] = true
				for j := range s.Calls {
					if strings.HasPrefix(s.Calls[j].key, ci+"/") {
						s.Calls[j].done = true
					}
				}
			}
		}
	}
	return nil
}

func (s *Stream) responses() error {
	items := map[string]string{}
	types := map[string]string{}
	completed := map[string]bool{}
	for i, f := range s.frames {
		if f.value == nil && !f.done {
			continue
		}
		if s.finished {
			return fmt.Errorf("OpenAI Responses event after terminal completion")
		}
		if f.done {
			return fmt.Errorf("unsupported [DONE] in Responses event stream")
		}
		m := f.value
		kind := text(m["type"])
		switch kind {
		case "response.output_item.added":
			key, err := index(m["output_index"])
			if err != nil {
				return err
			}
			if _, ok := items[key]; ok {
				return fmt.Errorf("duplicate Responses output item")
			}
			item := object(m["item"])
			items[key] = text(item["id"])
			if items[key] == "" {
				return fmt.Errorf("Responses output item requires identity")
			}
			types[key] = text(item["type"])
			if item["type"] == "function_call" {
				call, err := s.add(key, text(item["call_id"]), text(item["name"]))
				if err != nil {
					return err
				}
				if text(item["arguments"]) != "" {
					if err = call.fragment(i, item, "arguments"); err != nil {
						return err
					}
				}
			}
		case "response.function_call_arguments.delta", "response.function_call_arguments.done":
			key, err := index(m["output_index"])
			if err != nil {
				return err
			}
			call, err := s.call(key)
			if err != nil {
				return err
			}
			if items[key] == "" || items[key] != text(m["item_id"]) {
				return fmt.Errorf("Responses delta has mismatched item identity")
			}
			if kind == "response.function_call_arguments.delta" {
				if err = call.fragment(i, m, "delta"); err != nil {
					return err
				}
			} else {
				if call.argumentsDone || call.done {
					return fmt.Errorf("duplicate Responses argument completion")
				}
				if err = call.complete(i, m, "arguments", false); err != nil {
					return err
				}
				call.argumentsDone = true
			}
		case "response.output_item.done":
			key, err := index(m["output_index"])
			if err != nil {
				return err
			}
			item := object(m["item"])
			if completed[key] {
				return fmt.Errorf("duplicate Responses output item completion")
			}
			if items[key] == "" || text(item["id"]) != items[key] || text(item["type"]) != types[key] {
				return fmt.Errorf("Responses output completion has mismatched identity")
			}
			if item["type"] == "function_call" {
				call, err := s.call(key)
				if err != nil {
					return err
				}
				if call.done || !call.argumentsDone {
					return fmt.Errorf("duplicate or premature Responses item completion")
				}
				if text(item["call_id"]) != call.ID || text(item["name"]) != call.Name {
					return fmt.Errorf("Responses completed tool identity changed")
				}
				if err = call.complete(i, item, "arguments", false); err != nil {
					return err
				}
				call.done = true
			}
			completed[key] = true
		case "response.completed":
			for key := range items {
				if !completed[key] {
					return fmt.Errorf("Responses output item lacks completion")
				}
			}
			response := object(m["response"])
			if response == nil {
				return fmt.Errorf("Responses completion lacks response")
			}
			if id := text(response["id"]); id != "" {
				if s.ResponseID != "" && s.ResponseID != id {
					return fmt.Errorf("Responses response identity changed")
				}
				s.ResponseID = id
			}
			if status := text(response["status"]); status != "" && status != "completed" {
				return fmt.Errorf("Responses stream did not complete")
			}
			count := 0
			seen := map[string]bool{}
			for _, v := range array(response["output"]) {
				item := object(v)
				if item["type"] != "function_call" {
					continue
				}
				count++
				id := text(item["call_id"])
				if seen[id] {
					return fmt.Errorf("duplicate tool in Responses final snapshot")
				}
				seen[id] = true
				var call *Call
				for j := range s.Calls {
					if s.Calls[j].ID == id {
						call = &s.Calls[j]
						break
					}
				}
				if call == nil || !call.done || call.Name != text(item["name"]) {
					return fmt.Errorf("Responses final snapshot contradicts streamed tool")
				}
				if err := call.complete(i, item, "arguments", false); err != nil {
					return err
				}
			}
			if count != len(s.Calls) {
				return fmt.Errorf("Responses final snapshot is missing streamed calls")
			}
			s.Usage = map[string]any{"usage": response["usage"]}
			s.finished = true
		case "response.failed", "response.incomplete", "error":
			return fmt.Errorf("OpenAI Responses stream ended with %s", kind)
		case "response.created", "response.in_progress":
			if id := text(object(m["response"])["id"]); id != "" {
				if s.ResponseID != "" && s.ResponseID != id {
					return fmt.Errorf("Responses response identity changed")
				}
				s.ResponseID = id
			}
		default:
			allowed := false
			for _, prefix := range []string{"response.output_text.", "response.content_part.", "response.reasoning_summary_", "response.reasoning_text.", "response.refusal."} {
				allowed = allowed || strings.HasPrefix(kind, prefix)
			}
			if !allowed {
				return fmt.Errorf("unsupported OpenAI Responses event %q", kind)
			}
		}
	}
	return nil
}
