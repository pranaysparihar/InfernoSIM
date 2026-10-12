package toolstream

import "fmt"

func (s *Stream) anthropic() error {
	started := false
	messageDelta := false
	blocks := map[string]string{}
	stopped := map[string]bool{}
	starts := map[string]location{}
	usage := map[string]any{}
	for i, f := range s.frames {
		if f.value == nil && !f.done {
			continue
		}
		if s.finished {
			return fmt.Errorf("Anthropic event after message_stop")
		}
		if f.done {
			return fmt.Errorf("unsupported Anthropic terminal marker")
		}
		m := f.value
		kind := text(m["type"])
		if kind == "ping" {
			continue
		}
		if kind != "message_start" && !started {
			return fmt.Errorf("Anthropic event precedes message_start")
		}
		switch kind {
		case "message_start":
			if started {
				return fmt.Errorf("duplicate Anthropic message_start")
			}
			started = true
			for k, v := range object(object(m["message"])["usage"]) {
				usage[k] = v
			}
		case "content_block_start":
			key, err := index(m["index"])
			if err != nil {
				return err
			}
			if _, ok := blocks[key]; ok {
				return fmt.Errorf("duplicate Anthropic content block")
			}
			block := object(m["content_block"])
			blocks[key] = text(block["type"])
			if block["type"] == "tool_use" {
				if object(block["input"]) == nil {
					return fmt.Errorf("Anthropic tool start requires input object")
				}
				starts[key] = location{frame: i, object: block, field: "input", jsonObject: true}
				call, err := s.add(key, text(block["id"]), text(block["name"]))
				if err != nil {
					return err
				}
				if input := object(block["input"]); len(input) > 0 {
					if err = call.complete(i, block, "input", true); err != nil {
						return err
					}
					call.argumentsDone = true
				}
			} else if block["type"] != "text" && block["type"] != "thinking" && block["type"] != "redacted_thinking" {
				return fmt.Errorf("unsupported Anthropic block type %q", text(block["type"]))
			}
		case "content_block_delta":
			key, err := index(m["index"])
			if err != nil {
				return err
			}
			if blocks[key] == "" || stopped[key] {
				return fmt.Errorf("Anthropic delta outside active block")
			}
			delta := object(m["delta"])
			if blocks[key] == "tool_use" {
				if delta["type"] != "input_json_delta" {
					return fmt.Errorf("unsupported Anthropic tool delta")
				}
				call, err := s.call(key)
				if err != nil {
					return err
				}
				if err = call.fragment(i, delta, "partial_json"); err != nil {
					return err
				}
			}
		case "content_block_stop":
			key, err := index(m["index"])
			if err != nil {
				return err
			}
			if blocks[key] == "" || stopped[key] {
				return fmt.Errorf("duplicate or missing Anthropic block completion")
			}
			stopped[key] = true
			if blocks[key] == "tool_use" {
				call, err := s.call(key)
				if err != nil {
					return err
				}
				if len(call.Arguments) == 0 {
					if len(call.locations) > 0 {
						// An empty input can be accompanied by empty deltas. Keep
						// the delta locations as the mutation target: changing the
						// start object as well would complete arguments before them.
						call.Arguments = []byte("{}")
					} else {
						start := starts[key]
						if err = call.complete(start.frame, start.object, start.field, true); err != nil {
							return err
						}
					}
				}
				call.done = true
			}
		case "message_delta":
			if messageDelta {
				return fmt.Errorf("duplicate Anthropic message_delta")
			}
			messageDelta = true
			reason := text(object(m["delta"])["stop_reason"])
			if reason != "tool_use" && reason != "end_turn" && reason != "stop_sequence" {
				return fmt.Errorf("Anthropic stream ended with %s", reason)
			}
			for k, v := range object(m["usage"]) {
				usage[k] = v
			}
		case "message_stop":
			if !messageDelta {
				return fmt.Errorf("Anthropic message_stop lacks completion reason")
			}
			for key := range blocks {
				if !stopped[key] {
					return fmt.Errorf("Anthropic content block missing stop")
				}
			}
			s.finished = true
			s.Usage = map[string]any{"usage": usage}
		case "error":
			return fmt.Errorf("Anthropic error event")
		default:
			return fmt.Errorf("unsupported Anthropic event %q", kind)
		}
	}
	return nil
}
