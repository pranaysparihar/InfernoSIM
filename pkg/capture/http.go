package capture

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"infernosim/pkg/event"
	"infernosim/pkg/inject"
	"infernosim/pkg/privacy"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
)

const maxBodySize = 256 * 1024 // 256KB

// ProxyContext holds the dependencies for starting proxies
type ProxyContext struct {
	Logger  *event.Logger
	CA      *CAStore
	Inject  *inject.InjectConfig
	UseMITM bool
	// AllowInsecureUpstream disables TLS verification for outbound upstream connections.
	// Only set this when the --insecure-upstream CLI flag is explicitly passed.
	AllowInsecureUpstream bool
	// TunnelIdleTimeout is the maximum idle duration for a proxied CONNECT tunnel.
	// Defaults to 5 minutes if zero.
	TunnelIdleTimeout time.Duration
	// AllowPrivateDestinations permits loopback, link-local, and private
	// upstream addresses. It is intended for explicit local development only.
	AllowPrivateDestinations bool
	// CaptureSensitiveData stores raw headers and bodies. The secure default
	// redacts credentials and omits payload bytes while retaining fingerprints.
	CaptureSensitiveData bool
	// Privacy applies configurable redaction/tokenization before data is
	// written. A policy may explicitly permit storage of transformed bodies.
	Privacy *privacy.Policy
}

type replayReadCloser struct {
	io.Reader
	closer io.Closer
}

func (r *replayReadCloser) Close() error { return r.closer.Close() }

type idleDeadlineConn struct {
	net.Conn
	timeout time.Duration
}

func (c *idleDeadlineConn) Read(p []byte) (int, error) {
	_ = c.Conn.SetReadDeadline(time.Now().Add(c.timeout))
	return c.Conn.Read(p)
}

func (c *idleDeadlineConn) Write(p []byte) (int, error) {
	_ = c.Conn.SetWriteDeadline(time.Now().Add(c.timeout))
	return c.Conn.Write(p)
}

// peekBody snapshots at most maxBodySize bytes while preserving the complete
// original stream for forwarding. Memory usage is bounded even for arbitrarily
// large or streaming request bodies.
func peekBody(rc io.ReadCloser) (logSnap []byte, truncated bool, restoredRC io.ReadCloser, err error) {
	if rc == nil {
		return nil, false, nil, nil
	}
	prefix, readErr := io.ReadAll(io.LimitReader(rc, int64(maxBodySize)+1))
	if readErr != nil {
		_ = rc.Close()
		return nil, false, nil, readErr
	}
	truncated = len(prefix) > maxBodySize
	logSnap = prefix
	if truncated {
		logSnap = prefix[:maxBodySize]
	}
	restoredRC = &replayReadCloser{
		Reader: io.MultiReader(bytes.NewReader(prefix), rc),
		closer: rc,
	}
	return logSnap, truncated, restoredRC, nil
}

func isBlockedIP(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsPrivate() || ip.IsUnspecified()
}

// dialDestination resolves once, validates the resolved address, and dials
// that exact IP. This avoids a DNS check/dial time-of-check race.
func dialDestination(ctx context.Context, network, address, defaultPort string, allowPrivate bool) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		host = strings.Trim(address, "[]")
		port = defaultPort
	}
	if host == "" || port == "" {
		return nil, fmt.Errorf("invalid destination %q", address)
	}

	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("resolve %s: %w", host, err)
	}
	var blocked []string
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	for _, addr := range addrs {
		if !allowPrivate && isBlockedIP(addr.IP) {
			blocked = append(blocked, addr.IP.String())
			continue
		}
		conn, dialErr := dialer.DialContext(ctx, network, net.JoinHostPort(addr.IP.String(), port))
		if dialErr == nil {
			return conn, nil
		}
		err = dialErr
	}
	if len(blocked) > 0 && err == nil {
		return nil, fmt.Errorf("destination %s resolves only to blocked address ranges: %s", host, strings.Join(blocked, ", "))
	}
	if err != nil {
		return nil, err
	}
	return nil, fmt.Errorf("destination %s has no usable addresses", host)
}

func newUpstreamTransport(allowPrivate, insecure bool) *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.Proxy = nil
	t.DialContext = func(dialCtx context.Context, network, address string) (net.Conn, error) {
		return dialDestination(dialCtx, network, address, "80", allowPrivate)
	}
	if insecure {
		t.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // explicit CLI opt-in
	}
	return t
}

var (
	publicVerifiedTransport  = newUpstreamTransport(false, false)
	publicInsecureTransport  = newUpstreamTransport(false, true)
	privateVerifiedTransport = newUpstreamTransport(true, false)
	privateInsecureTransport = newUpstreamTransport(true, true)
)

func upstreamTransport(ctx *ProxyContext) *http.Transport {
	switch {
	case ctx.AllowPrivateDestinations && ctx.AllowInsecureUpstream:
		return privateInsecureTransport
	case ctx.AllowPrivateDestinations:
		return privateVerifiedTransport
	case ctx.AllowInsecureUpstream:
		return publicInsecureTransport
	default:
		return publicVerifiedTransport
	}
}

var sensitiveHeaderNames = map[string]struct{}{
	"authorization":       {},
	"cookie":              {},
	"proxy-authorization": {},
	"set-cookie":          {},
	"x-api-key":           {},
}

func headersForLog(h http.Header, captureSensitive bool, policy *privacy.Policy) http.Header {
	out := cloneHeaders(h)
	if policy != nil {
		out = policy.ApplyHeaders(out)
	}
	if captureSensitive {
		return out
	}
	for name := range out {
		if _, sensitive := sensitiveHeaderNames[strings.ToLower(name)]; sensitive {
			if _, configured := policy.HeaderRule(name); configured {
				continue
			}
			out[name] = []string{"[REDACTED]"}
		}
	}
	return out
}

func payloadForLog(body []byte, ctx *ProxyContext) (payload []byte, store bool, transformed bool) {
	payload = append([]byte(nil), body...)
	store = ctx.CaptureSensitiveData
	if ctx.Privacy == nil {
		return payload, store, false
	}
	processed, err := ctx.Privacy.ApplyBody(payload)
	if err != nil {
		log.Printf("privacy policy omitted body: %v", err)
		return nil, false, true
	}
	return processed, store || ctx.Privacy.CaptureBodies, !bytes.Equal(processed, body)
}

func urlForLog(input *url.URL, policy *privacy.Policy) string {
	if input == nil {
		return ""
	}
	if policy == nil {
		return input.String()
	}
	return policy.ApplyURL(input).String()
}

func extractGRPCStatus(resp *http.Response) string {
	if resp == nil {
		return ""
	}
	status := resp.Header.Get("Grpc-Status")
	if status == "" && resp.Trailer != nil {
		status = resp.Trailer.Get("Grpc-Status")
	}
	return status
}

// StartInboundProxy starts a reverse proxy that listens on listenAddr and forwards to targetURL.
func StartInboundProxy(listenAddr string, targetURL *url.URL, ctx *ProxyContext) (*http.Server, error) {
	proxy := httputil.NewSingleHostReverseProxy(targetURL)
	proxy.ModifyResponse = func(resp *http.Response) error {
		statusCode := resp.StatusCode
		req := resp.Request

		corrID := event.SanitizeTraceID(req.Header.Get("X-Inferno-TraceID"))
		if corrID == "" {
			corrID = event.GenerateID()
		}

		// Read response body
		bodyBytes, truncated, newRc, _ := peekBody(resp.Body)
		resp.Body = newRc

		logBody, storeBody, transformed := payloadForLog(bodyBytes, ctx)
		evt := &event.Event{
			ID:        event.GenerateID(),
			Type:      "InboundResponse",
			Timestamp: time.Now().UTC(),
			Service:   targetURL.Host,
			Method:    req.Method,
			URL:       urlForLog(req.URL, ctx.Privacy),
			Status:    statusCode,
			TraceID:   corrID,
			Headers:   headersForLog(resp.Header, ctx.CaptureSensitiveData, ctx.Privacy),
		}

		if len(logBody) > 0 {
			hash := sha256.Sum256(logBody)
			evt.BodySha256 = hex.EncodeToString(hash[:])
			evt.BodyTruncated = truncated
			evt.BodyRedacted = !storeBody || transformed
			if !truncated && storeBody {
				evt.BodyB64 = base64.StdEncoding.EncodeToString(logBody)
			}
			evt.BytesSent = int64(len(bodyBytes))
			if resp.ContentLength >= 0 {
				evt.BytesSent = resp.ContentLength
			}
		}

		if IsGRPCRequest(req) {
			evt.GrpcServiceMethod = req.URL.Path
			evt.GrpcStatus = extractGRPCStatus(resp)
		}

		writeEvent(ctx.Logger, evt)
		log.Printf("Logged response for inbound request %s -> %d", req.URL.Path, statusCode)
		return nil
	}

	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		originalDirector(req)

		if req.Header.Get("X-Inferno-TraceID") == "" {
			traceID := event.GenerateID()
			req.Header.Set("X-Inferno-TraceID", traceID)
		}

		// Read request body
		bodyBytes, truncated, newRc, _ := peekBody(req.Body)
		req.Body = newRc

		logBody, storeBody, transformed := payloadForLog(bodyBytes, ctx)
		evt := &event.Event{
			ID:        event.GenerateID(),
			Type:      "InboundRequest",
			Timestamp: time.Now().UTC(),
			Service:   targetURL.Host,
			Method:    req.Method,
			URL:       urlForLog(req.URL, ctx.Privacy),
			Headers:   headersForLog(req.Header, ctx.CaptureSensitiveData, ctx.Privacy),
			BodySize:  req.ContentLength,
			TraceID:   req.Header.Get("X-Inferno-TraceID"),
		}

		if len(logBody) > 0 {
			hash := sha256.Sum256(logBody)
			evt.BodySha256 = hex.EncodeToString(hash[:])
			evt.BodyTruncated = truncated
			evt.BodyRedacted = !storeBody || transformed
			if !truncated && storeBody {
				evt.BodyB64 = base64.StdEncoding.EncodeToString(logBody)
			}
			evt.BytesReceived = int64(len(bodyBytes))
			if req.ContentLength >= 0 {
				evt.BytesReceived = req.ContentLength
			}
		}

		if IsGRPCRequest(req) {
			evt.GrpcServiceMethod = req.URL.Path
		}

		writeEvent(ctx.Logger, evt)
		log.Printf("Logged inbound request %s %s", req.Method, req.URL.Path)
	}

	listener, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return nil, err
	}
	server := &http.Server{
		Addr:              listener.Addr().String(),
		Handler:           h2c.NewHandler(proxy, &http2.Server{}),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    1 << 20,
	}

	go func() {
		if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
			log.Printf("Inbound proxy server error: %v", err)
		}
	}()
	return server, nil
}

// StartForwardProxy starts a forward (outbound) proxy on listenAddr.
func StartForwardProxy(listenAddr string, ctx *ProxyContext) (*http.Server, error) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method == http.MethodConnect {
			if ctx.UseMITM && ctx.CA != nil {
				mitmConnect(w, req, ctx)
			} else {
				tunnelConnect(w, req, ctx)
			}
		} else {
			handleHTTP(w, req, ctx)
		}
	})

	listener, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return nil, err
	}
	server := &http.Server{
		Addr:              listener.Addr().String(),
		Handler:           h2c.NewHandler(handler, &http2.Server{}),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    1 << 20,
	}

	go func() {
		if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
			log.Printf("Forward proxy server error: %v", err)
		}
	}()
	return server, nil
}

func handleHTTP(w http.ResponseWriter, req *http.Request, ctx *ProxyContext) {
	startTime := time.Now().UTC()

	// Evaluate injection
	action := ctx.Inject.Evaluate(true)

	if action.Delay > 0 {
		time.Sleep(action.Delay)
	}

	if action.Drop {
		// Just drop the connection silently
		hj, ok := w.(http.Hijacker)
		if ok {
			conn, _, _ := hj.Hijack()
			if conn != nil {
				conn.Close()
			}
			return
		}
	}

	if action.Reset {
		// Send RST if possible, or just close abruptly
		hj, ok := w.(http.Hijacker)
		if ok {
			conn, _, _ := hj.Hijack()
			if conn != nil {
				if tcpConn, ok := conn.(*net.TCPConn); ok {
					tcpConn.SetLinger(0)
				}
				conn.Close()
			}
			return
		}
	}

	if action.Status > 0 {
		w.WriteHeader(action.Status)

		evt := &event.Event{
			ID:               event.GenerateID(),
			Type:             "OutboundCall",
			Timestamp:        startTime,
			Method:           req.Method,
			URL:              req.URL.String(),
			Status:           action.Status,
			Duration:         time.Since(startTime),
			InjectionApplied: action.Applied,
		}
		writeEvent(ctx.Logger, evt)
		return
	}

	if req.URL == nil {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}
	if !req.URL.IsAbs() {
		req.URL.Scheme = "http"
		req.URL.Host = req.Host
	}

	bodyBytes, truncated, newRc, _ := peekBody(req.Body)

	outReq, err := http.NewRequest(req.Method, req.URL.String(), newRc)
	if err != nil {
		http.Error(w, "Bad Gateway", http.StatusBadGateway)
		return
	}

	outReq.Header = cloneHeaders(req.Header)
	outReq.Header.Del("Proxy-Connection")
	outReq.Header.Del("Proxy-Authenticate")
	outReq.Header.Del("Proxy-Authorization")
	// RFC 7230 §6.1: remove all headers named in the Connection header.
	for _, field := range strings.Split(outReq.Header.Get("Connection"), ",") {
		outReq.Header.Del(strings.TrimSpace(field))
	}
	// Remove standard hop-by-hop headers unconditionally.
	for _, h := range []string{"Connection", "Keep-Alive", "Transfer-Encoding", "Upgrade", "Trailer", "TE"} {
		outReq.Header.Del(h)
	}

	var resp *http.Response
	if IsGRPCRequest(req) {
		// gRPC requires an explicit HTTP/2 transport. For HTTPS, retain the
		// validated destination dial and then perform a real TLS handshake;
		// returning a raw socket here would silently break gRPC over TLS.
		proxyCtx := ctx
		useTLS := strings.EqualFold(outReq.URL.Scheme, "https")
		defaultPort := "80"
		if useTLS {
			defaultPort = "443"
		}
		t2 := &http2.Transport{
			AllowHTTP: !useTLS,
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: proxyCtx.AllowInsecureUpstream, //nolint:gosec // explicit CLI opt-in
				MinVersion:         tls.VersionTLS12,
				NextProtos:         []string{"h2"},
			},
			DialTLSContext: func(dialCtx context.Context, network, addr string, cfg *tls.Config) (net.Conn, error) {
				rawConn, dialErr := dialDestination(dialCtx, network, addr, defaultPort, proxyCtx.AllowPrivateDestinations)
				if dialErr != nil || !useTLS {
					return rawConn, dialErr
				}
				tlsConfig := &tls.Config{}
				if cfg != nil {
					tlsConfig = cfg.Clone()
				}
				tlsConfig.InsecureSkipVerify = proxyCtx.AllowInsecureUpstream //nolint:gosec // explicit CLI opt-in
				tlsConfig.MinVersion = tls.VersionTLS12
				tlsConfig.NextProtos = []string{"h2"}
				if tlsConfig.ServerName == "" {
					tlsConfig.ServerName = outReq.URL.Hostname()
				}
				tlsConn := tls.Client(rawConn, tlsConfig)
				if handshakeErr := tlsConn.HandshakeContext(dialCtx); handshakeErr != nil {
					_ = rawConn.Close()
					return nil, handshakeErr
				}
				return tlsConn, nil
			},
		}
		resp, err = t2.RoundTrip(outReq)
	} else {
		resp, err = upstreamTransport(ctx).RoundTrip(outReq)
	}

	var statusCode int
	var respBodyBytes []byte
	var respBodyTruncated bool
	var grpcStatus string

	if err != nil {
		log.Printf("Error forwarding request to %s: %v", req.URL, err)
		statusCode = 0
		http.Error(w, "Bad Gateway", http.StatusBadGateway)
	} else {
		statusCode = resp.StatusCode
		respBodyBytes, respBodyTruncated, resp.Body, _ = peekBody(resp.Body)
		copyResponse(w, resp)
		if IsGRPCRequest(req) {
			grpcStatus = extractGRPCStatus(resp)
		}
		resp.Body.Close()
	}

	logRequestBody, storeRequestBody, requestTransformed := payloadForLog(bodyBytes, ctx)
	logResponseBody, storeResponseBody, responseTransformed := payloadForLog(respBodyBytes, ctx)
	evt := &event.Event{
		ID:               event.GenerateID(),
		Type:             "OutboundCall",
		Timestamp:        startTime,
		Method:           req.Method,
		URL:              urlForLog(req.URL, ctx.Privacy),
		Headers:          headersForLog(req.Header, ctx.CaptureSensitiveData, ctx.Privacy),
		BodySize:         req.ContentLength,
		Status:           statusCode,
		Duration:         time.Since(startTime),
		InjectionApplied: action.Applied,
	}
	evt.ResponseBodyTruncated = respBodyTruncated

	if err != nil {
		evt.Error = err.Error()
	}

	evt.BodyTruncated = truncated

	if len(logRequestBody) > 0 {
		hash := sha256.Sum256(logRequestBody)
		evt.BodySha256 = hex.EncodeToString(hash[:])
		evt.BodyRedacted = !storeRequestBody || requestTransformed
		if !truncated && storeRequestBody {
			evt.BodyB64 = base64.StdEncoding.EncodeToString(logRequestBody)
		}
		evt.BytesSent = int64(len(bodyBytes))
		if req.ContentLength >= 0 {
			evt.BytesSent = req.ContentLength
		}
	}

	if len(logResponseBody) > 0 && evt.Error == "" {
		evt.BytesReceived = int64(len(respBodyBytes))
		if resp.ContentLength >= 0 {
			evt.BytesReceived = resp.ContentLength
		}
		hash := sha256.Sum256(logResponseBody)
		evt.ResponseBodySha256 = hex.EncodeToString(hash[:])
		evt.ResponseBodyRedacted = !storeResponseBody || responseTransformed
		if storeResponseBody && !respBodyTruncated {
			evt.ResponseBodyB64 = base64.StdEncoding.EncodeToString(logResponseBody)
		}
	}
	if resp != nil {
		evt.ResponseHeaders = headersForLog(resp.Header, ctx.CaptureSensitiveData, ctx.Privacy)
		evt.ResponseTrailers = headersForLog(resp.Trailer, ctx.CaptureSensitiveData, ctx.Privacy)
		evt.ResponseCaptured = true
	}

	if IsGRPCRequest(req) {
		evt.GrpcServiceMethod = req.URL.Path
		evt.GrpcStatus = grpcStatus
	}

	writeEvent(ctx.Logger, evt)
	log.Printf("Logged outbound call: %s %s -> %d", req.Method, req.URL, statusCode)
}

func tunnelConnect(w http.ResponseWriter, req *http.Request, ctx *ProxyContext) {
	startTime := time.Now().UTC()
	dest := req.Host

	// Evaluate CONNECT level injection (no status overrides, only delay/drop/reset)
	action := ctx.Inject.Evaluate(false)

	if action.Delay > 0 {
		time.Sleep(action.Delay)
	}

	if action.Drop || action.Reset {
		http.Error(w, "Connection failed", http.StatusServiceUnavailable)
		logEventTunnel(ctx.Logger, startTime, dest, 0, action.Applied, "Injected drop/reset")
		return
	}

	log.Printf("Handling CONNECT (tunnel) to %s", dest)
	dialCtx, cancel := context.WithTimeout(req.Context(), 10*time.Second)
	targetConn, err := dialDestination(dialCtx, "tcp", dest, "443", ctx.AllowPrivateDestinations)
	cancel()
	if err != nil {
		http.Error(w, "Destination unavailable", http.StatusServiceUnavailable)
		logEventTunnel(ctx.Logger, startTime, dest, 0, action.Applied, err.Error())
		return
	}

	hijacker, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "Proxy error", http.StatusInternalServerError)
		targetConn.Close()
		logEventTunnel(ctx.Logger, startTime, dest, 500, action.Applied, "Hijack failed")
		return
	}

	clientConn, _, err := hijacker.Hijack()
	if err != nil {
		log.Println("Hijack error:", err)
		targetConn.Close()
		logEventTunnel(ctx.Logger, startTime, dest, 500, action.Applied, err.Error())
		return
	}
	if _, err := io.WriteString(clientConn, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		_ = clientConn.Close()
		_ = targetConn.Close()
		logEventTunnel(ctx.Logger, startTime, dest, 0, action.Applied, err.Error())
		return
	}

	tunnelTimeout := 5 * time.Minute
	if ctx.TunnelIdleTimeout > 0 {
		tunnelTimeout = ctx.TunnelIdleTimeout
	}
	idleTarget := &idleDeadlineConn{Conn: targetConn, timeout: tunnelTimeout}
	idleClient := &idleDeadlineConn{Conn: clientConn, timeout: tunnelTimeout}
	go func() {
		defer targetConn.Close()
		defer clientConn.Close()
		_, _ = io.Copy(idleTarget, idleClient)
	}()
	go func() {
		defer targetConn.Close()
		defer clientConn.Close()
		_, _ = io.Copy(idleClient, idleTarget)
	}()

	logEventTunnel(ctx.Logger, startTime, dest, 200, action.Applied, "")
	log.Printf("Logged outbound CONNECT to %s", dest)
}

func logEventTunnel(logger *event.Logger, start time.Time, dest string, status int, applied string, errStr string) {
	evt := &event.Event{
		ID:               event.GenerateID(),
		Type:             "OutboundCall",
		Timestamp:        time.Now().UTC(),
		Method:           "CONNECT",
		URL:              dest,
		Status:           status,
		Duration:         time.Since(start),
		InjectionApplied: applied,
		Error:            errStr,
	}
	writeEvent(logger, evt)
}

func mitmConnect(w http.ResponseWriter, req *http.Request, ctx *ProxyContext) {
	startTime := time.Now().UTC()
	dest := req.Host
	host, _, err := net.SplitHostPort(dest)
	if err != nil {
		host = dest
	}

	log.Printf("Handling CONNECT (MITM) to %s", dest)

	// VULN-002: Only issue a MITM cert for explicitly allowlisted hosts.
	if ctx.CA != nil && !ctx.CA.isAllowed(host) {
		log.Printf("MITM cert refused for non-allowlisted host: %s", host)
		http.Error(w, "MITM not permitted for this host", http.StatusForbidden)
		return
	}

	hijacker, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "Proxy error", http.StatusInternalServerError)
		return
	}

	clientConn, _, err := hijacker.Hijack()
	if err != nil {
		log.Println("Hijack error:", err)
		return
	}
	if _, err := io.WriteString(clientConn, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		_ = clientConn.Close()
		return
	}

	// Generate leaf cert dynamically
	cert, err := ctx.CA.GenerateLeafCert(host)
	if err != nil {
		log.Printf("MITM cert gen failed for %s: %v", host, err)
		clientConn.Close()
		return
	}

	tlsConfig := &tls.Config{
		Certificates: []tls.Certificate{*cert},
		NextProtos:   []string{"h2", "http/1.1"},
	}

	tlsConn := tls.Server(clientConn, tlsConfig)
	if err := tlsConn.Handshake(); err != nil {
		log.Printf("MITM TLS handshake failed for %s: %v", host, err)
		tlsConn.Close()
		return
	}

	mitmHandler := http.HandlerFunc(func(mw http.ResponseWriter, mreq *http.Request) {
		mreq.URL.Scheme = "https"
		mreq.URL.Host = dest
		mitmCtx := *ctx
		// An explicitly MITM-allowlisted host is also an explicit authorization
		// to connect to that host when it resolves to a local/private address.
		mitmCtx.AllowPrivateDestinations = true
		handleHTTP(mw, mreq, &mitmCtx)
	})

	go func() {
		if tlsConn.ConnectionState().NegotiatedProtocol == "h2" {
			h2Server := &http2.Server{}
			h2Server.ServeConn(tlsConn, &http2.ServeConnOpts{Handler: mitmHandler})
			return
		}
		server := &http.Server{Handler: mitmHandler}
		if err := server.Serve(&singleConnListener{conn: tlsConn}); err != nil && err != http.ErrServerClosed {
			log.Printf("MITM connection server error: %v", err)
		}
	}()

	logEventTunnel(ctx.Logger, startTime, dest, 200, "", "MITM Tunnel Established")
}

// singleConnListener allows us to run standard http.Serve over a single hijacked connection
type singleConnListener struct {
	conn net.Conn
	done bool
}

func (s *singleConnListener) Accept() (net.Conn, error) {
	if s.done {
		return nil, io.EOF
	}
	s.done = true
	return s.conn, nil
}

func (s *singleConnListener) Close() error   { return nil }
func (s *singleConnListener) Addr() net.Addr { return s.conn.LocalAddr() }

func copyResponse(w http.ResponseWriter, resp *http.Response) {
	for k, vals := range resp.Header {
		if strings.ToLower(k) == "connection" {
			continue
		}
		for _, v := range vals {
			w.Header().Add(k, v)
		}
	}
	for name := range resp.Trailer {
		w.Header().Add("Trailer", name)
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
	for name, values := range resp.Trailer {
		w.Header()[name] = append([]string(nil), values...)
	}
}

func cloneHeaders(h http.Header) http.Header {
	c := make(http.Header, len(h))
	for k, v := range h {
		c[k] = append([]string(nil), v...)
	}
	return c
}

func writeEvent(logger *event.Logger, evt *event.Event) {
	if logger == nil {
		return
	}
	if err := logger.Write(evt); err != nil {
		log.Printf("event log write failed: %v", err)
	}
}
