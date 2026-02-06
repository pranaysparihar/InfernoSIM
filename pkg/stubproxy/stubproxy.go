package stubproxy

import (
	"bufio"
	"encoding/json"
	"fmt"
	"infernosim/pkg/event"
	"infernosim/pkg/inject"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type StubProxy struct {
	events []event.Event
	i      int64
	seen   int64
	maxSeen int64

	rules []inject.Rule

	// per-dep attempt counters (for retry-limit behavior)
	attempts map[string]int

	mu                 sync.Mutex
	divergenceReasons  []string
	unexpectedOutbound bool

	observedLogger *event.Logger

	forwardErrors  int64
	forwardSuccess int64
	cycleExpected  bool
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

func New(outboundLog string, observedLog string, rules []inject.Rule) (*StubProxy, error) {
	evs, err := LoadOutboundEvents(outboundLog)
	if err != nil {
		return nil, err
	}
	var observedLogger *event.Logger
	if observedLog != "" {
		observedLogger, _ = event.NewLogger(observedLog)
	}
	return &StubProxy{
		events:         evs,
		rules:          rules,
		attempts:       map[string]int{},
		observedLogger: observedLogger,
	}, nil
}

// Reset resets per-run state so the same captured outbound
// sequence can be replayed deterministically across runs.
func (s *StubProxy) Reset() {
	atomic.StoreInt64(&s.i, 0)
	atomic.StoreInt64(&s.seen, 0)
	atomic.StoreInt64(&s.maxSeen, 0)
	s.attempts = map[string]int{}
	s.mu.Lock()
	s.divergenceReasons = nil
	s.unexpectedOutbound = false
	s.mu.Unlock()
}

// ConfigureReplayCardinality controls how many outbound events this run may observe.
// When cycleExpected is true, expected events are matched in a repeating pattern.
func (s *StubProxy) ConfigureReplayCardinality(cycleExpected bool, maxObserved int) {
	s.cycleExpected = cycleExpected
	if maxObserved < 0 {
		maxObserved = 0
	}
	atomic.StoreInt64(&s.maxSeen, int64(maxObserved))
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
	msg := fmt.Sprintf(
		"DIVERGENCE at outbound event index=%d why=%s expected={method=%s url=%s} got={method=%s url=%s host=%s}",
		idx, why,
		expected.Method, expected.URL,
		got.Method, got.URL.String(), got.Host,
	)
	fmt.Fprintln(os.Stderr, msg)
	s.mu.Lock()
	s.divergenceReasons = append(s.divergenceReasons, msg)
	s.mu.Unlock()
}

func (s *StubProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	idx := atomic.LoadInt64(&s.i)
	seen := atomic.AddInt64(&s.seen, 1)

	observedHost := r.Host
	if r.URL != nil && r.URL.Host != "" {
		observedHost = r.URL.Host
	}
	s.recordObserved(r.Method, observedHost, "", 0)

	maxSeen := atomic.LoadInt64(&s.maxSeen)
	if maxSeen > 0 && seen > maxSeen {
		msg := fmt.Sprintf("DIVERGENCE at outbound event index=%d why=unexpected_outbound_call", idx)
		fmt.Fprintln(os.Stderr, msg)
		s.mu.Lock()
		s.divergenceReasons = append(s.divergenceReasons, msg)
		s.unexpectedOutbound = true
		s.mu.Unlock()
		http.Error(w, "unexpected outbound call", http.StatusBadGateway)
		return
	}
	if len(s.events) == 0 {
		http.Error(w, "no captured outbound events", http.StatusBadGateway)
		return
	}

	expected := s.events[idx%int64(len(s.events))]
	if !s.cycleExpected && int(idx) >= len(s.events) {
		msg := fmt.Sprintf("DIVERGENCE at outbound event index=%d why=unexpected_outbound_call", idx)
		fmt.Fprintln(os.Stderr, msg)
		s.mu.Lock()
		s.divergenceReasons = append(s.divergenceReasons, msg)
		s.unexpectedOutbound = true
		s.mu.Unlock()
		http.Error(w, "unexpected outbound call", http.StatusBadGateway)
		return
	}

	// --- basic matching (v0 tolerant) ---
	if expected.Method != "" && r.Method != expected.Method {
		s.divergence(expected, r, idx, "method_mismatch")
	}

	gotURL := r.URL.String()
	if expected.URL != "" &&
		!strings.Contains(gotURL, strings.TrimPrefix(expected.URL, "http://")) {
		s.divergence(expected, r, idx, "url_mismatch")
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

func (s *StubProxy) forwardProxyRequest(w http.ResponseWriter, r *http.Request) error {
	if r.URL == nil || !r.URL.IsAbs() {
		return fmt.Errorf("absolute-form URL required")
	}

	scheme := r.URL.Scheme
	host := r.URL.Host
	if host == "" {
		host = r.Host
	}
	if host == "" {
		return fmt.Errorf("missing host")
	}

	targetURL := &url.URL{
		Scheme:   scheme,
		Host:     host,
		Path:     r.URL.Path,
		RawQuery: r.URL.RawQuery,
	}
	req, err := http.NewRequest(r.Method, targetURL.String(), r.Body)
	if err != nil {
		return err
	}
	copyHeaders(req.Header, r.Header)
	req.Host = host

	client := &http.Client{
		Transport: &http.Transport{
			Proxy:               nil,
			DisableKeepAlives:   true,
			MaxIdleConns:        0,
			MaxIdleConnsPerHost: 1,
		},
	}

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	copyHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	if _, err := io.Copy(w, resp.Body); err != nil {
		return err
	}

	s.recordObserved(r.Method, host, "", 0, resp.StatusCode)
	return nil
}

func (s *StubProxy) ServeTransparent(listener net.Listener) error {
	for {
		conn, err := listener.Accept()
		if err != nil {
			return err
		}
		go s.handleTransparent(conn)
	}
}

func (s *StubProxy) handleTransparent(conn net.Conn) {
	defer conn.Close()

	tcpConn, ok := conn.(*net.TCPConn)
	if !ok {
		return
	}

	ip, port, err := originalDst(tcpConn)
	if err != nil {
		return
	}

	reader := bufio.NewReader(conn)
	req, err := http.ReadRequest(reader)
	if err != nil {
		s.recordObserved("UNKNOWN", "", ip, port)
		return
	}
	_ = req.Body.Close()

	host := req.Host
	s.recordObserved(req.Method, host, ip, port)

	idx := atomic.LoadInt64(&s.i)
	atomic.AddInt64(&s.seen, 1)

	if int(idx) >= len(s.events) {
		msg := fmt.Sprintf("DIVERGENCE at outbound event index=%d why=unexpected_outbound_call", idx)
		fmt.Fprintln(os.Stderr, msg)
		s.mu.Lock()
		s.divergenceReasons = append(s.divergenceReasons, msg)
		s.unexpectedOutbound = true
		s.mu.Unlock()
		writeSimpleResponse(conn, http.StatusBadGateway)
		return
	}

	expected := s.events[idx]
	atomic.AddInt64(&s.i, 1)

	dep := host
	if dep == "" {
		dep = fmt.Sprintf("%s:%d", ip, port)
	}

	rule := inject.Match(depKeyFromHost(dep), s.rules)

	if rule != nil && rule.Timeout > 0 {
		time.Sleep(rule.Timeout)
		writeSimpleResponse(conn, http.StatusGatewayTimeout)
		return
	}
	if rule != nil && rule.AddLatency > 0 {
		time.Sleep(rule.AddLatency)
	}
	if rule != nil && rule.RetryLimit >= 0 {
		s.attempts[dep]++
		if s.attempts[dep] <= rule.RetryLimit {
			writeSimpleResponse(conn, http.StatusBadGateway)
			return
		}
	}

	status := expected.Status
	if status == 0 {
		writeSimpleResponse(conn, http.StatusBadGateway)
		return
	}
	writeSimpleResponse(conn, status)
}

func writeSimpleResponse(w io.Writer, status int) {
	reason := http.StatusText(status)
	if reason == "" {
		reason = "Status"
	}
	fmt.Fprintf(w, "HTTP/1.1 %d %s\r\nContent-Length: 0\r\n\r\n", status, reason)
}

func depKeyFromHost(host string) string {
	if strings.Contains(host, ":") {
		h, _, err := net.SplitHostPort(host)
		if err == nil {
			return h
		}
	}
	return host
}

func (s *StubProxy) recordObserved(method, host, ip string, port int, status ...int) {
	if s.observedLogger == nil {
		return
	}
	url := ""
	if ip != "" && port > 0 {
		url = fmt.Sprintf("tcp://%s:%d", ip, port)
	} else if host != "" {
		url = fmt.Sprintf("http://%s", host)
	}
	e := &event.Event{
		ID:       event.GenerateID(),
		Type:     "OutboundCall",
		Method:   method,
		URL:      url,
		Service:  host,
		Duration: 0,
		Status:   0,
	}
	if len(status) > 0 {
		e.Status = status[0]
	}
	_ = s.observedLogger.Write(e)
}

func (s *StubProxy) ObservedCount() int {
	return int(atomic.LoadInt64(&s.seen))
}

func (s *StubProxy) ForwardErrors() int {
	return int(atomic.LoadInt64(&s.forwardErrors))
}

func (s *StubProxy) ForwardSuccess() int {
	return int(atomic.LoadInt64(&s.forwardSuccess))
}

func (s *StubProxy) ExpectedCount() int {
	return len(s.events)
}

func (s *StubProxy) DivergenceReasons() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.divergenceReasons))
	copy(out, s.divergenceReasons)
	return out
}

func (s *StubProxy) UnexpectedOutbound() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.unexpectedOutbound
}

var hopByHopHeaders = map[string]struct{}{
	"Connection":          {},
	"Proxy-Connection":    {},
	"Keep-Alive":          {},
	"Proxy-Authenticate":  {},
	"Proxy-Authorization": {},
	"Te":                  {},
	"Trailer":             {},
	"Transfer-Encoding":   {},
	"Upgrade":             {},
}

func copyHeaders(dst, src http.Header) {
	for k, vals := range src {
		if _, skip := hopByHopHeaders[k]; skip {
			continue
		}
		for _, v := range vals {
			dst.Add(k, v)
		}
	}
}
