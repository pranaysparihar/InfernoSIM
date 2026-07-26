package stubproxy

import (
	"bufio"
	"encoding/base64"
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
	events  []event.Event
	i       int64
	seen    int64
	maxSeen int64

	rules []inject.Rule

	// per-dep attempt counters (for retry-limit behavior)
	attempts   map[string]int
	attemptsMu sync.Mutex

	mu                 sync.Mutex
	divergenceReasons  []string
	unexpectedOutbound bool

	observedLogger *event.Logger

	forwardErrors  int64
	forwardSuccess int64
	cycleExpected  bool

	matchMu         sync.Mutex
	eventsByKey     map[string][]event.Event
	matchCounts     map[string]int
	matchMultiplier int
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
		if !os.IsNotExist(err) {
			return nil, err
		}
		evs = nil
	}
	var observedLogger *event.Logger
	if observedLog != "" {
		observedLogger, err = event.NewLogger(observedLog)
		if err != nil {
			return nil, err
		}
	}
	eventsByKey := make(map[string][]event.Event)
	for _, evt := range evs {
		key := eventMatchKey(evt)
		eventsByKey[key] = append(eventsByKey[key], evt)
	}
	return &StubProxy{
		events:          evs,
		rules:           rules,
		attempts:        map[string]int{},
		observedLogger:  observedLogger,
		eventsByKey:     eventsByKey,
		matchCounts:     make(map[string]int),
		matchMultiplier: 1,
	}, nil
}

// Reset resets per-run state so the same captured outbound
// sequence can be replayed deterministically across runs.
func (s *StubProxy) Reset() {
	atomic.StoreInt64(&s.i, 0)
	atomic.StoreInt64(&s.seen, 0)
	atomic.StoreInt64(&s.maxSeen, 0)
	s.attemptsMu.Lock()
	s.attempts = map[string]int{}
	s.attemptsMu.Unlock()
	s.mu.Lock()
	s.divergenceReasons = nil
	s.unexpectedOutbound = false
	s.mu.Unlock()
	s.matchMu.Lock()
	s.matchCounts = make(map[string]int)
	s.matchMu.Unlock()
}

// ConfigureReplayCardinality controls how many outbound events this run may observe.
// When cycleExpected is true, expected events are matched in a repeating pattern.
func (s *StubProxy) ConfigureReplayCardinality(cycleExpected bool, maxObserved int) {
	s.cycleExpected = cycleExpected
	if maxObserved < 0 {
		maxObserved = 0
	}
	atomic.StoreInt64(&s.maxSeen, int64(maxObserved))
	s.matchMu.Lock()
	s.matchMultiplier = 1
	if cycleExpected && len(s.events) > 0 {
		s.matchMultiplier = (maxObserved + len(s.events) - 1) / len(s.events)
		if s.matchMultiplier < 1 {
			s.matchMultiplier = 1
		}
	}
	s.matchMu.Unlock()
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
	seen := atomic.AddInt64(&s.seen, 1)

	observedHost := r.Host
	if r.URL != nil && r.URL.Host != "" {
		observedHost = r.URL.Host
	}
	s.recordObserved(r.Method, observedHost, "", 0)

	maxSeen := atomic.LoadInt64(&s.maxSeen)
	if maxSeen > 0 && seen > maxSeen {
		msg := fmt.Sprintf("DIVERGENCE at outbound event index=%d why=unexpected_outbound_call", seen-1)
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

	expected, _, matched := s.matchExpected(r)
	if !matched {
		msg := fmt.Sprintf(
			"DIVERGENCE at outbound event index=%d why=no_matching_captured_call got={method=%s url=%s host=%s}",
			seen-1,
			r.Method,
			r.URL.String(),
			r.Host,
		)
		fmt.Fprintln(os.Stderr, msg)
		s.mu.Lock()
		s.divergenceReasons = append(s.divergenceReasons, msg)
		s.unexpectedOutbound = true
		s.mu.Unlock()
		http.Error(w, "unexpected outbound call", http.StatusBadGateway)
		return
	}

	dep := depKey(r)
	s.attemptsMu.Lock()
	s.attempts[dep]++
	attemptCount := s.attempts[dep]
	s.attemptsMu.Unlock()

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
		if attemptCount <= rule.RetryLimit {
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

	copyHeaders(w.Header(), http.Header(expected.ResponseHeaders))
	body, bodyErr := base64.StdEncoding.DecodeString(expected.ResponseBodyB64)
	if bodyErr != nil {
		http.Error(w, "captured dependency body is invalid", http.StatusBadGateway)
		return
	}
	w.WriteHeader(status)
	if len(body) > 0 {
		_, _ = w.Write(body)
	}
}

func (s *StubProxy) matchExpected(r *http.Request) (event.Event, int64, bool) {
	key := requestMatchKey(r)
	s.matchMu.Lock()
	defer s.matchMu.Unlock()

	candidates := s.eventsByKey[key]
	if len(candidates) == 0 {
		return event.Event{}, 0, false
	}
	count := s.matchCounts[key]
	limit := len(candidates) * s.matchMultiplier
	if count >= limit {
		return event.Event{}, int64(count), false
	}
	s.matchCounts[key] = count + 1
	return candidates[count%len(candidates)], int64(count), true
}

func eventMatchKey(e event.Event) string {
	parsed, err := url.Parse(e.URL)
	if err != nil {
		return strings.ToUpper(e.Method) + " " + e.URL
	}
	return canonicalMatchKey(e.Method, parsed.Host, parsed.EscapedPath(), parsed.RawQuery)
}

func requestMatchKey(r *http.Request) string {
	host := r.Host
	if r.URL != nil && r.URL.Host != "" {
		host = r.URL.Host
	}
	path := "/"
	query := ""
	if r.URL != nil {
		if r.URL.EscapedPath() != "" {
			path = r.URL.EscapedPath()
		}
		query = r.URL.RawQuery
	}
	return canonicalMatchKey(r.Method, host, path, query)
}

func canonicalMatchKey(method, host, path, query string) string {
	if path == "" {
		path = "/"
	}
	if query != "" {
		path += "?" + query
	}
	return strings.ToUpper(method) + " " + strings.ToLower(host) + path
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
		s.attemptsMu.Lock()
		s.attempts[dep]++
		attemptCount := s.attempts[dep]
		s.attemptsMu.Unlock()
		if attemptCount <= rule.RetryLimit {
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
