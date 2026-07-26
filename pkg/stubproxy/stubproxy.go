package stubproxy

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"infernosim/pkg/capture"
	"infernosim/pkg/event"
	"infernosim/pkg/inject"
	"infernosim/pkg/matcher"
	"infernosim/pkg/scenario"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
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

	semanticMatcher *matcher.Matcher
	eventUseCounts  map[int]int
	scenarios       *scenario.Engine
	tlsCA           *capture.CAStore
}

type Options struct {
	Matching  matcher.Config
	Scenarios []scenario.Config
	TLSCA     *capture.CAStore
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
	return NewWithOptions(outboundLog, observedLog, rules, Options{})
}

func NewWithOptions(outboundLog string, observedLog string, rules []inject.Rule, opts Options) (*StubProxy, error) {
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
	semanticMatcher, err := matcher.New(opts.Matching)
	if err != nil {
		return nil, err
	}
	scenarioEngine, err := scenario.New(opts.Scenarios)
	if err != nil {
		return nil, err
	}
	return &StubProxy{
		events:          evs,
		rules:           rules,
		attempts:        map[string]int{},
		observedLogger:  observedLogger,
		eventsByKey:     eventsByKey,
		matchCounts:     make(map[string]int),
		matchMultiplier: 1,
		semanticMatcher: semanticMatcher,
		eventUseCounts:  make(map[int]int),
		scenarios:       scenarioEngine,
		tlsCA:           opts.TLSCA,
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
	s.eventUseCounts = make(map[int]int)
	s.matchMu.Unlock()
	s.scenarios.Reset()
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
	if r.Method == http.MethodConnect {
		if s.tlsCA == nil {
			http.Error(w, "HTTPS stubbing is not enabled", http.StatusNotImplemented)
			return
		}
		s.serveTLSConnect(w, r)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 16*1024*1024+1))
	if err != nil {
		http.Error(w, "could not read request body", http.StatusBadRequest)
		return
	}
	if len(body) > 16*1024*1024 {
		http.Error(w, "request body exceeds 16 MiB safety limit", http.StatusRequestEntityTooLarge)
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(body))

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
	if result, matched := s.scenarios.Match(r, body); matched {
		responseBody, bodyErr := result.Response.Bytes()
		if bodyErr != nil {
			http.Error(w, "scenario response body is invalid", http.StatusBadGateway)
			return
		}
		headers := http.Header(result.Response.Headers).Clone()
		if headers == nil {
			headers = make(http.Header)
		}
		headers.Set("X-Inferno-Scenario", result.Scenario)
		grpcStatus := result.Response.GRPCStatus
		if isGRPCRequest(r) && grpcStatus == "" {
			grpcStatus = "0"
		}
		writeStubResponse(w, result.Response.Status, headers, http.Header(result.Response.Trailers), grpcStatus, responseBody)
		return
	}
	if len(s.events) == 0 {
		http.Error(w, "no captured outbound events or matching scenario", http.StatusBadGateway)
		return
	}

	expected, _, matched := s.matchExpected(r, body)
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

	body, bodyErr := base64.StdEncoding.DecodeString(expected.ResponseBodyB64)
	if bodyErr != nil {
		http.Error(w, "captured dependency body is invalid", http.StatusBadGateway)
		return
	}
	grpcStatus := expected.GrpcStatus
	if isGRPCRequest(r) && grpcStatus == "" {
		grpcStatus = "0"
	}
	writeStubResponse(
		w,
		status,
		http.Header(expected.ResponseHeaders),
		http.Header(expected.ResponseTrailers),
		grpcStatus,
		body,
	)
}

func isGRPCRequest(r *http.Request) bool {
	return strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/grpc")
}

func writeStubResponse(
	w http.ResponseWriter,
	status int,
	headers http.Header,
	trailers http.Header,
	grpcStatus string,
	body []byte,
) {
	copyHeaders(w.Header(), headers)
	trailerValues := trailers.Clone()
	if trailerValues == nil {
		trailerValues = make(http.Header)
	}
	if grpcStatus != "" && trailerValues.Get("Grpc-Status") == "" {
		trailerValues.Set("Grpc-Status", grpcStatus)
	}
	for name := range trailerValues {
		w.Header().Add("Trailer", name)
	}
	w.WriteHeader(status)
	if len(body) > 0 {
		_, _ = w.Write(body)
	}
	for name, values := range trailerValues {
		w.Header()[name] = append([]string(nil), values...)
	}
}

// Handler serves both HTTP/1.1 and cleartext HTTP/2 (h2c), which is required
// for plaintext gRPC dependency virtualization.
func (s *StubProxy) Handler() http.Handler {
	return h2c.NewHandler(s, &http2.Server{})
}

func (s *StubProxy) matchExpected(r *http.Request, body []byte) (event.Event, int64, bool) {
	s.matchMu.Lock()
	defer s.matchMu.Unlock()

	for index, candidate := range s.events {
		matched, _ := s.semanticMatcher.Match(candidate, r, body)
		if !matched {
			continue
		}
		if s.eventUseCounts[index] >= s.matchMultiplier {
			continue
		}
		count := s.eventUseCounts[index]
		s.eventUseCounts[index] = count + 1
		return candidate, int64(index), true
	}
	return event.Event{}, 0, false
}

func (s *StubProxy) serveTLSConnect(w http.ResponseWriter, r *http.Request) {
	host := r.Host
	if parsedHost, _, err := net.SplitHostPort(host); err == nil {
		host = parsedHost
	}
	host = strings.Trim(host, "[]")
	if !s.tlsCA.IsAllowed(host) {
		http.Error(w, "HTTPS stub host is not allowlisted", http.StatusForbidden)
		return
	}
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "HTTPS stub requires connection hijacking", http.StatusInternalServerError)
		return
	}
	clientConn, _, err := hijacker.Hijack()
	if err != nil {
		return
	}
	if _, err := io.WriteString(clientConn, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		_ = clientConn.Close()
		return
	}
	cert, err := s.tlsCA.GenerateLeafCert(host)
	if err != nil {
		_ = clientConn.Close()
		return
	}
	tlsConn := tls.Server(clientConn, &tls.Config{
		Certificates: []tls.Certificate{*cert},
		MinVersion:   tls.VersionTLS12,
		NextProtos:   []string{"h2", "http/1.1"},
	})
	if err := tlsConn.Handshake(); err != nil {
		_ = tlsConn.Close()
		return
	}
	destination := r.Host
	handler := http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		request.URL.Scheme = "https"
		request.URL.Host = destination
		request.Host = destination
		s.ServeHTTP(response, request)
	})
	server := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
	if tlsConn.ConnectionState().NegotiatedProtocol == "h2" {
		h2Server := &http2.Server{}
		h2Server.ServeConn(tlsConn, &http2.ServeConnOpts{Handler: handler})
		return
	}
	if err := server.Serve(&tlsSingleConnListener{conn: tlsConn}); err != nil && err != http.ErrServerClosed && err != io.EOF {
		fmt.Fprintf(os.Stderr, "HTTPS stub connection error: %v\n", err)
	}
}

type tlsSingleConnListener struct {
	conn net.Conn
	done bool
}

func (l *tlsSingleConnListener) Accept() (net.Conn, error) {
	if l.done {
		return nil, io.EOF
	}
	l.done = true
	return l.conn, nil
}

func (l *tlsSingleConnListener) Close() error   { return nil }
func (l *tlsSingleConnListener) Addr() net.Addr { return l.conn.LocalAddr() }

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
