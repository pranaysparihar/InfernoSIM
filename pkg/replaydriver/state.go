package replaydriver

import (
	"encoding/base64"
	"infernosim/pkg/event"
	"net/http"
	"strings"
	"sync"
)

type RuntimeState struct {
	mu     sync.RWMutex
	values map[string]string // CapturedValue -> RuntimeValue
}

func NewRuntimeState() *RuntimeState {
	return &RuntimeState{
		values: make(map[string]string),
	}
}

func (s *RuntimeState) Put(old, new string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.values[old] = new
}

func (s *RuntimeState) Get(old string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.values[old]
	return v, ok
}

type RequestRewriter struct {
	state         *RuntimeState
	consumedIndex map[string][]ConsumedValue // locator -> all consumed values across all captured events
}

func NewRequestRewriter(state *RuntimeState) *RequestRewriter {
	return &RequestRewriter{state: state, consumedIndex: make(map[string][]ConsumedValue)}
}

// NewRequestRewriterWithEvents builds a RequestRewriter pre-seeded with a consumed-value index
// derived from all captured events. This enables UpdateState to correctly map
// old captured values -> new live values when chained dependencies are replayed.
func NewRequestRewriterWithEvents(state *RuntimeState, events []event.Event) *RequestRewriter {
	index := make(map[string][]ConsumedValue)
	for _, e := range events {
		for _, c := range IdentifyConsumers(e) {
			index[c.Locator] = append(index[c.Locator], c)
		}
	}
	return &RequestRewriter{state: state, consumedIndex: index}
}

// Rewrite applies state substitutions to a request being replayed
func (rw *RequestRewriter) Rewrite(e event.Event, req *http.Request) {
	// 1. Rewrite headers (two-pass to avoid mutating map during iteration)
	type headerMod struct {
		key   string
		index int
		value string
	}
	var mods []headerMod
	for k, vals := range req.Header {
		for i, v := range vals {
			if newVal := rw.substitute(v); newVal != v {
				mods = append(mods, headerMod{k, i, newVal})
			}
		}
	}
	for _, m := range mods {
		req.Header[m.key][m.index] = m.value
	}

	// 2. Rewrite URL path and query
	req.URL.Path = rw.substitute(req.URL.Path)
	req.URL.RawQuery = rw.substitute(req.URL.RawQuery)
}

func (rw *RequestRewriter) substitute(input string) string {
	rw.state.mu.RLock()
	defer rw.state.mu.RUnlock()

	output := input
	for old, newValue := range rw.state.values {
		if old == "" {
			continue
		}
		output = strings.ReplaceAll(output, old, newValue)
	}
	return output
}

// PrepareBody handles body substitution and returns the new reader
func (rw *RequestRewriter) PrepareBody(e event.Event) ([]byte, bool) {
	if e.BodyB64 == "" {
		return nil, false
	}

	body, err := base64.StdEncoding.DecodeString(e.BodyB64)
	if err != nil {
		return nil, false
	}

	// Apply substitutions to body bytes (casting to string for simplicity)
	bodyStr := rw.substitute(string(body))
	return []byte(bodyStr), true
}

// UpdateState processes a replayed response to extract new values and map them to their
// captured counterparts. It uses the pre-built consumedValueIndex (from all captured events)
// to find what "old" value each produced value should replace.
func (rw *RequestRewriter) UpdateState(captured event.Event, resp *http.Response, bodyBytes []byte) {
	produced := ExtractResponseValues(resp, bodyBytes)
	for _, p := range produced {
		consumers, ok := rw.consumedIndex[p.Locator]
		if !ok {
			continue
		}
		for _, c := range consumers {
			if c.Kind == p.Kind && c.Value != p.Value {
				rw.state.Put(c.Value, p.Value)
			}
		}
	}
}
