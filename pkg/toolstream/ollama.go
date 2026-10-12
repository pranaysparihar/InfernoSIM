package toolstream

import (
	"fmt"
	"strconv"
)

func (s *Stream) ollama() error {
	for i, f := range s.frames {
		if f.value == nil && !f.done {
			continue
		}
		if s.finished {
			return fmt.Errorf("Ollama event after done")
		}
		if f.done {
			return fmt.Errorf("unsupported Ollama terminal marker")
		}
		m := f.value
		if _, ok := m["error"]; ok {
			return fmt.Errorf("Ollama error event")
		}
		message := object(m["message"])
		if message == nil {
			return fmt.Errorf("unsupported Ollama response shape")
		}
		for _, v := range array(message["tool_calls"]) {
			entry := object(v)
			function := object(entry["function"])
			key := ""
			if n, ok := function["index"]; ok {
				var err error
				key, err = index(n)
				if err != nil {
					return err
				}
			} else {
				key = strconv.Itoa(len(s.Calls))
			}
			id := text(entry["id"])
			if id == "" {
				id = "ollama:" + key
			}
			call, err := s.add(key, id, text(function["name"]))
			if err != nil {
				return err
			}
			if err = call.complete(i, function, "arguments", true); err != nil {
				return err
			}
			call.done = true
		}
		done, ok := m["done"].(bool)
		if !ok {
			return fmt.Errorf("Ollama chunk lacks done boolean")
		}
		if done {
			reason := text(m["done_reason"])
			if reason != "" && reason != "stop" {
				return fmt.Errorf("Ollama stream ended with %s", reason)
			}
			s.finished = true
			s.Usage = m
		}
	}
	return nil
}
