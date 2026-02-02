package stubproxy

import (
	"encoding/json"
	"fmt"
	"infernosim/pkg/event"
	"infernosim/pkg/inject"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"time"
)

type StubProxy struct {
	events []event.Event
	i      int64

	rules []inject.Rule

	// per-dep attempt counters (for retry-limit behavior)
	attempts map[string]int
}

func LoadOutboundEvents(path string) ([]event.Event, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	dec := json.NewDecoder(f)

	var out []event.Event
	for {
		var e event.Event
		if err := dec.Decode(&e); err != nil {
			if err == io.EOF {
				break
			}
			return nil, fmt.Errorf("outbound log parse error: %w", err)
		}
		if e.Type == "OutboundCall" {
			out = append(out, e)
		}
	}

	return out, nil
}

func New(outboundLog string, rules []inject.Rule) (*StubProxy, error) {
	evs, err := LoadOutboundEvents(outboundLog)
	if err != nil {
		return nil, err
	}
	return &StubProxy{
		events:   evs,
		rules:    rules,
		attempts: map[string]int{},
	}, nil
}

// Reset resets per-run state so the same captured outbound
// sequence can be replayed deterministically across runs.
func (s *StubProxy) Reset() {
	atomic.StoreInt64(&s.i, 0)
	s.attempts = map[string]int{}
}

// depKey returns a stable dependency identifier from a proxied request.
// For HTTP proxy requests, req.URL.Host is usually set. Fallback to req.Host.
func depKey(r *http.Request) string {
	host := r.URL.Host
	if host == "" {
		host = r.Host
	}
	if strings.Contains(host, ":") {
		h, _, err := net.SplitHostPort(host)
		if err == nil {
			return h
		}
	}
	return host
}

func (s *StubProxy) divergence(expected event.Event, got *http.Request, idx int64, why string) {
	fmt.Fprintf(os.Stderr,
		"DIVERGENCE at outbound event index=%d why=%s expected={method=%s url=%s} got={method=%s url=%s host=%s}\n",
		idx, why,
		expected.Method, expected.URL,
		got.Method, got.URL.String(), got.Host,
	)
	os.Exit(2)
}

func (s *StubProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	idx := atomic.LoadInt64(&s.i)

	if int(idx) >= len(s.events) {
		fmt.Fprintf(os.Stderr,
			"DIVERGENCE at outbound event index=%d why=unexpected_outbound_call\n",
			idx,
		)
		os.Exit(2)
	}

	expected := s.events[idx]

	// --- basic matching (v0 tolerant) ---
	if expected.Method != "" && r.Method != expected.Method {
		s.divergence(expected, r, idx, "method_mismatch")
	}

	gotURL := r.URL.String()
	if expected.URL != "" &&
		!strings.Contains(gotURL, strings.TrimPrefix(expected.URL, "http://")) {
		// tolerant match for v0
	}

	atomic.AddInt64(&s.i, 1)

	dep := depKey(r)
	s.attempts[dep]++

	rule := inject.Match(dep, s.rules)

	// --- TIMEOUT INJECTION ---
	if rule != nil && rule.Timeout > 0 {
		time.Sleep(rule.Timeout)
		http.Error(w, "injected timeout", http.StatusGatewayTimeout)
		return
	}

	// --- LATENCY INJECTION ---
	if rule != nil && rule.AddLatency > 0 {
		time.Sleep(rule.AddLatency)
	}

	// --- RETRY COUNT MODIFICATION ---
	if rule != nil && rule.RetryLimit >= 0 {
		if s.attempts[dep] <= rule.RetryLimit {
			http.Error(w, "injected retry-failure", http.StatusBadGateway)
			return
		}
	}

	// --- DEFAULT: replay captured outcome ---
	status := expected.Status
	if status == 0 {
		http.Error(w, "captured error replayed", http.StatusBadGateway)
		return
	}

	w.WriteHeader(status)
}
