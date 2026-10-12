// Package toolstream assembles bounded recorded provider streams and rewrites
// one tool's arguments while preserving unrelated frames byte for byte.
package toolstream

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

const MaximumBytes = 16 << 20
const MaximumFrames = 10000
const MaximumCalls = 256

type Call struct {
	ID, Name      string
	Arguments     []byte
	key           string
	locations     []location
	done          bool
	argumentsDone bool
}
type location struct {
	frame      int
	object     map[string]any
	field      string
	fragment   bool
	jsonObject bool
}
type span struct{ start, end int }
type frame struct {
	raw     []byte
	payload []span
	value   map[string]any
	done    bool
	changed bool
}
type Stream struct {
	ResponseID string
	Calls      []Call
	Usage      any
	frames     []frame
	provider   string
	finished   bool
}

func object(v any) map[string]any { m, _ := v.(map[string]any); return m }
func array(v any) []any           { a, _ := v.([]any); return a }
func text(v any) string           { s, _ := v.(string); return s }
func decode(b []byte) (any, error) {
	var v any
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	if err := d.Decode(&v); err != nil {
		return nil, err
	}
	if !json.Valid(b) {
		return nil, fmt.Errorf("invalid JSON")
	}
	return v, nil
}
func canonical(v any) string { b, _ := json.Marshal(v); return string(b) }
func index(v any) (string, error) {
	n, ok := v.(json.Number)
	if !ok {
		return "", fmt.Errorf("missing integer stream index")
	}
	i, err := strconv.Atoi(string(n))
	if err != nil || i < 0 || i > 10000 {
		return "", fmt.Errorf("invalid stream index")
	}
	return strconv.Itoa(i), nil
}

func parseFrames(data []byte, sse bool) ([]frame, error) {
	if len(data) == 0 || len(data) > MaximumBytes {
		return nil, fmt.Errorf("provider stream must contain 1..%d bytes", MaximumBytes)
	}
	var frames []frame
	for len(data) > 0 {
		if len(frames) >= MaximumFrames {
			return nil, fmt.Errorf("provider stream exceeds %d frames", MaximumFrames)
		}
		end := 0
		if sse {
			a := bytes.Index(data, []byte("\n\n"))
			b := bytes.Index(data, []byte("\r\n\r\n"))
			if a >= 0 {
				end = a + 2
			}
			if b >= 0 && (end == 0 || b+4 < end) {
				end = b + 4
			}
			if end == 0 {
				return nil, fmt.Errorf("truncated SSE frame")
			}
		} else {
			if i := bytes.IndexByte(data, '\n'); i >= 0 {
				end = i + 1
			} else {
				return nil, fmt.Errorf("truncated NDJSON frame")
			}
		}
		f := frame{raw: append([]byte(nil), data[:end]...)}
		data = data[end:]
		var payload []byte
		if sse {
			for offset := 0; offset < len(f.raw); {
				e := bytes.IndexByte(f.raw[offset:], '\n')
				if e < 0 {
					e = len(f.raw) - offset
				} else {
					e++
				}
				line := bytes.TrimSuffix(bytes.TrimSuffix(f.raw[offset:offset+e], []byte("\n")), []byte("\r"))
				if bytes.HasPrefix(line, []byte("data:")) {
					start := 5
					if start < len(line) && line[start] == ' ' {
						start++
					}
					if len(f.payload) > 0 {
						payload = append(payload, '\n')
					}
					payload = append(payload, line[start:]...)
					f.payload = append(f.payload, span{offset + start, offset + len(line)})
				}
				offset += e
			}
		} else {
			body := bytes.TrimSpace(f.raw)
			if len(body) > 0 {
				start := bytes.Index(f.raw, body)
				f.payload = []span{{start, start + len(body)}}
				payload = body
			}
		}
		if len(f.payload) > 0 {
			if bytes.Equal(bytes.TrimSpace(payload), []byte("[DONE]")) {
				f.done = true
			} else {
				v, err := decode(payload)
				if err != nil {
					return nil, fmt.Errorf("invalid provider stream JSON at frame %d: %w", len(frames), err)
				}
				f.value = object(v)
				if f.value == nil {
					return nil, fmt.Errorf("provider stream frame must be an object")
				}
			}
		}
		frames = append(frames, f)
	}
	return frames, nil
}

func (s *Stream) add(key, id, name string) (*Call, error) {
	if key == "" || id == "" || name == "" {
		return nil, fmt.Errorf("tool stream requires index, identity and name")
	}
	if len(s.Calls) >= MaximumCalls {
		return nil, fmt.Errorf("too many streamed tool calls")
	}
	for _, c := range s.Calls {
		if c.key == key || c.ID == id {
			return nil, fmt.Errorf("duplicate streamed tool identity or index")
		}
	}
	s.Calls = append(s.Calls, Call{ID: id, Name: name, key: key})
	return &s.Calls[len(s.Calls)-1], nil
}
func (s *Stream) call(key string) (*Call, error) {
	for i := range s.Calls {
		if s.Calls[i].key == key {
			return &s.Calls[i], nil
		}
	}
	return nil, fmt.Errorf("tool delta/completion precedes its start")
}
func (c *Call) fragment(f int, m map[string]any, field string) error {
	if c.done || c.argumentsDone {
		return fmt.Errorf("tool arguments continued after completion")
	}
	delta, ok := m[field].(string)
	if !ok {
		return fmt.Errorf("tool argument delta must be a string")
	}
	if len(c.Arguments)+len(delta) > MaximumBytes {
		return fmt.Errorf("tool argument limit exceeded")
	}
	c.Arguments = append(c.Arguments, delta...)
	c.locations = append(c.locations, location{frame: f, object: m, field: field, fragment: true})
	return nil
}
func (c *Call) complete(f int, m map[string]any, field string, isObject bool) error {
	var value []byte
	if isObject {
		if object(m[field]) == nil {
			return fmt.Errorf("tool arguments must be an object")
		}
		value = []byte(canonical(m[field]))
	} else {
		v, ok := m[field].(string)
		if !ok {
			return fmt.Errorf("tool arguments must be a string")
		}
		value = []byte(v)
	}
	if len(c.Arguments) > 0 {
		a, err := decode(c.Arguments)
		if err != nil {
			return fmt.Errorf("truncated or malformed tool argument fragments")
		}
		b, err := decode(value)
		if err != nil || canonical(a) != canonical(b) {
			return fmt.Errorf("tool argument completion conflicts with deltas")
		}
	} else {
		c.Arguments = append([]byte(nil), value...)
	}
	c.locations = append(c.locations, location{frame: f, object: m, field: field, jsonObject: isObject})
	return nil
}

// Parse accepts documented SSE (OpenAI/Anthropic) and NDJSON (Ollama) event
// families. Missing or contradictory completion is invalid, even without faults.
func Parse(provider string, data []byte) (*Stream, error) {
	if provider != "openai" && provider != "anthropic" && provider != "ollama" {
		return nil, fmt.Errorf("unsupported provider stream %q", provider)
	}
	frames, err := parseFrames(data, provider != "ollama")
	if err != nil {
		return nil, err
	}
	s := &Stream{frames: frames, provider: provider}
	switch provider {
	case "openai":
		err = s.openAI()
	case "anthropic":
		err = s.anthropic()
	case "ollama":
		err = s.ollama()
	}
	if err != nil {
		return nil, err
	}
	if !s.finished {
		return nil, fmt.Errorf("provider stream is missing its terminal completion")
	}
	for _, c := range s.Calls {
		if !c.done {
			return nil, fmt.Errorf("tool %s is missing completion", c.ID)
		}
		v, err := decode(c.Arguments)
		if err != nil || object(v) == nil {
			return nil, fmt.Errorf("tool %s has malformed or truncated argument JSON", c.ID)
		}
	}
	return s, nil
}

func (s *Stream) ReplaceArguments(id string, mutate func([]byte) ([]byte, error)) error {
	var target *Call
	for i := range s.Calls {
		if s.Calls[i].ID == id {
			target = &s.Calls[i]
			break
		}
	}
	if target == nil {
		return fmt.Errorf("unknown streamed tool call %s", id)
	}
	value, err := mutate(append([]byte(nil), target.Arguments...))
	if err != nil {
		return err
	}
	if len(value) > MaximumBytes {
		return fmt.Errorf("mutated arguments exceed stream limit")
	}
	var decoded any
	hasObject := false
	for _, l := range target.locations {
		hasObject = hasObject || l.jsonObject
	}
	if hasObject {
		decoded, err = decode(value)
		if err != nil {
			return fmt.Errorf("provider requires JSON object arguments: %w", err)
		}
	}
	first := true
	for _, l := range target.locations {
		if l.fragment {
			if first {
				l.object[l.field] = string(value)
				first = false
			} else {
				l.object[l.field] = ""
			}
		} else if l.jsonObject {
			l.object[l.field] = decoded
		} else {
			l.object[l.field] = string(value)
		}
		s.frames[l.frame].changed = true
	}
	target.Arguments = append([]byte(nil), value...)
	return nil
}

func (s *Stream) Bytes() ([]byte, error) {
	var result bytes.Buffer
	for _, f := range s.frames {
		if !f.changed {
			result.Write(f.raw)
			continue
		}
		encoded, err := json.Marshal(f.value)
		if err != nil {
			return nil, err
		}
		offset := 0
		for i, p := range f.payload {
			result.Write(f.raw[offset:p.start])
			if i == 0 {
				result.Write(encoded)
			}
			offset = p.end
		}
		result.Write(f.raw[offset:])
		if result.Len() > MaximumBytes {
			return nil, fmt.Errorf("mutated stream exceeds size limit")
		}
	}
	if result.Len() > MaximumBytes {
		return nil, fmt.Errorf("mutated stream exceeds size limit")
	}
	return result.Bytes(), nil
}

func IsStream(contentType string) bool {
	v := strings.ToLower(contentType)
	return strings.Contains(v, "text/event-stream") || strings.Contains(v, "ndjson") || strings.Contains(v, "json-seq")
}

// DetectAndParse identifies only the supported provider event families in a
// recorded stream. It is used by graph discovery before a replay adapter exists.
func DetectAndParse(data []byte) (*Stream, error) {
	sse := bytes.HasPrefix(bytes.TrimSpace(data), []byte("data:")) || bytes.HasPrefix(bytes.TrimSpace(data), []byte("event:")) || bytes.HasPrefix(bytes.TrimSpace(data), []byte(":"))
	frames, err := parseFrames(data, sse)
	if err != nil {
		return nil, err
	}
	for _, f := range frames {
		m := f.value
		if m == nil {
			continue
		}
		kind := text(m["type"])
		if m["object"] == "chat.completion.chunk" || strings.HasPrefix(kind, "response.") {
			return Parse("openai", data)
		}
		if kind == "message_start" || kind == "ping" {
			return Parse("anthropic", data)
		}
		if !sse && m["message"] != nil && m["done"] != nil {
			return Parse("ollama", data)
		}
		return nil, fmt.Errorf("unsupported recorded provider stream event")
	}
	return nil, fmt.Errorf("empty recorded provider stream")
}
