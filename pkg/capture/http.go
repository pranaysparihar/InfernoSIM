package capture

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
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

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
)

var defaultUpstreamTransport = &http.Transport{
	TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
}

const maxBodySize = 256 * 1024 // 256KB

// ProxyContext holds the dependencies for starting proxies
type ProxyContext struct {
	Logger  *event.Logger
	CA      *CAStore
	Inject  *inject.InjectConfig
	UseMITM bool
}

// readBoundedBody reads up to maxBodySize, returns the body bytes, a bool if truncated, and replaces the original ReadCloser
func readBoundedBody(rc io.ReadCloser) ([]byte, bool, io.ReadCloser, error) {
	if rc == nil {
		return nil, false, nil, nil
	}
	// Read up to maxBodySize + 1 to detect truncation
	buf, err := io.ReadAll(io.LimitReader(rc, int64(maxBodySize)+1))
	rc.Close()

	truncated := false
	if len(buf) > maxBodySize {
		truncated = true
		buf = buf[:maxBodySize]
	}

	// Reconstruct the ReadCloser for the pipeline
	newRc := io.NopCloser(bytes.NewReader(buf))
	return buf, truncated, newRc, err
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

		corrID := req.Header.Get("X-Inferno-TraceID")
		if corrID == "" {
			corrID = event.GenerateID()
		}

		// Read response body
		bodyBytes, truncated, newRc, _ := readBoundedBody(resp.Body)
		resp.Body = newRc

		evt := &event.Event{
			ID:        event.GenerateID(),
			Type:      "InboundResponse",
			Timestamp: time.Now().UTC(),
			Service:   targetURL.Host,
			Method:    req.Method,
			URL:       req.URL.String(),
			Status:    statusCode,
			TraceID:   corrID,
			Headers:   cloneHeaders(resp.Header),
		}

		if len(bodyBytes) > 0 {
			hash := sha256.Sum256(bodyBytes)
			evt.BodySha256 = hex.EncodeToString(hash[:])
			evt.BodyTruncated = truncated
			if !truncated {
				evt.BodyB64 = base64.StdEncoding.EncodeToString(bodyBytes)
			}
			evt.BytesSent = int64(len(bodyBytes))
		}

		if IsGRPCRequest(req) {
			evt.GrpcServiceMethod = req.URL.Path
			evt.GrpcStatus = extractGRPCStatus(resp)
		}

		ctx.Logger.Write(evt)
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
		bodyBytes, truncated, newRc, _ := readBoundedBody(req.Body)
		req.Body = newRc

		evt := &event.Event{
			ID:        event.GenerateID(),
			Type:      "InboundRequest",
			Timestamp: time.Now().UTC(),
			Service:   targetURL.Host,
			Method:    req.Method,
			URL:       req.URL.String(),
			Headers:   cloneHeaders(req.Header),
			BodySize:  req.ContentLength,
			TraceID:   req.Header.Get("X-Inferno-TraceID"),
		}

		if len(bodyBytes) > 0 {
			hash := sha256.Sum256(bodyBytes)
			evt.BodySha256 = hex.EncodeToString(hash[:])
			evt.BodyTruncated = truncated
			if !truncated {
				evt.BodyB64 = base64.StdEncoding.EncodeToString(bodyBytes)
			}
			evt.BytesReceived = int64(len(bodyBytes))
		}

		if IsGRPCRequest(req) {
			evt.GrpcServiceMethod = req.URL.Path
		}

		ctx.Logger.Write(evt)
		log.Printf("Logged inbound request %s %s", req.Method, req.URL.Path)
	}

	server := &http.Server{
		Addr:    listenAddr,
		Handler: h2c.NewHandler(proxy, &http2.Server{}),
	}

	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Inbound proxy server error: %v", err)
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

	server := &http.Server{
		Addr:    listenAddr,
		Handler: h2c.NewHandler(handler, &http2.Server{}),
	}

	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Forward proxy server error: %v", err)
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
		ctx.Logger.Write(evt)
		return
	}

	bodyBytes, truncated, newRc, _ := readBoundedBody(req.Body)

	outReq, err := http.NewRequest(req.Method, req.URL.String(), newRc)
	if err != nil {
		http.Error(w, "Bad Gateway", http.StatusBadGateway)
		return
	}

	outReq.Header = cloneHeaders(req.Header)
	outReq.Header.Del("Proxy-Connection")
	outReq.Header.Del("Proxy-Authenticate")
	outReq.Header.Del("Proxy-Authorization")

	var resp *http.Response
	if IsGRPCRequest(req) {
		// gRPC requires an http2 explicit transport
		t2 := &http2.Transport{
			AllowHTTP: true,
			DialTLSContext: func(ctx context.Context, network, addr string, cfg *tls.Config) (net.Conn, error) {
				return net.Dial(network, addr) // Cleartext H2C support
			},
		}
		resp, err = t2.RoundTrip(outReq)
	} else {
		resp, err = defaultUpstreamTransport.RoundTrip(outReq)
	}

	var statusCode int
	var respBodyBytes []byte
	var grpcStatus string

	if err != nil {
		log.Printf("Error forwarding request to %s: %v", req.URL, err)
		statusCode = 0
	} else {
		statusCode = resp.StatusCode
		respBodyBytes, _, resp.Body, _ = readBoundedBody(resp.Body)
		if IsGRPCRequest(req) {
			grpcStatus = extractGRPCStatus(resp)
		}
		copyResponse(w, resp)
		resp.Body.Close()
	}

	evt := &event.Event{
		ID:               event.GenerateID(),
		Type:             "OutboundCall",
		Timestamp:        startTime,
		Method:           req.Method,
		URL:              req.URL.String(),
		Headers:          cloneHeaders(req.Header),
		BodySize:         req.ContentLength,
		Status:           statusCode,
		Duration:         time.Since(startTime),
		InjectionApplied: action.Applied,
	}

	if err != nil {
		evt.Error = err.Error()
	}

	evt.BodyTruncated = truncated

	if len(bodyBytes) > 0 {
		hash := sha256.Sum256(bodyBytes)
		evt.BodySha256 = hex.EncodeToString(hash[:])
		if !truncated {
			evt.BodyB64 = base64.StdEncoding.EncodeToString(bodyBytes)
		}
		evt.BytesSent = int64(len(bodyBytes))
	}

	if len(respBodyBytes) > 0 && evt.Error == "" {
		evt.BytesReceived = int64(len(respBodyBytes))
	}

	if IsGRPCRequest(req) {
		evt.GrpcServiceMethod = req.URL.Path
		evt.GrpcStatus = grpcStatus
	}

	ctx.Logger.Write(evt)
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
	targetConn, err := net.DialTimeout("tcp", dest, 10*time.Second)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		logEventTunnel(ctx.Logger, startTime, dest, 0, action.Applied, err.Error())
		return
	}

	w.WriteHeader(http.StatusOK)
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

	go func() {
		defer targetConn.Close()
		defer clientConn.Close()
		io.Copy(targetConn, clientConn)
	}()
	go func() {
		defer targetConn.Close()
		defer clientConn.Close()
		io.Copy(clientConn, targetConn)
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
	logger.Write(evt)
}

func mitmConnect(w http.ResponseWriter, req *http.Request, ctx *ProxyContext) {
	startTime := time.Now().UTC()
	dest := req.Host
	host, _, err := net.SplitHostPort(dest)
	if err != nil {
		host = dest
	}

	log.Printf("Handling CONNECT (MITM) to %s", dest)

	w.WriteHeader(http.StatusOK)
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

	if tlsConn.ConnectionState().NegotiatedProtocol == "h2" {
		// we could explicitly configure h2, but DefaultTransport handles h2 dials natively if negotiated
		// Let's implement a minimal reverse proxy mapping
	}

	// Disptach a custom proxy to read from the decrypted tlsConn and write to upstream.
	// Since we are intercepting standard HTTP inside the tunnel, we map it back to `handleHTTP` logic
	// But because `handleHTTP` expects a ResponseWriter and Request, we will spin up a local listener
	// or serve it via http.ServeConn natively.

	mitmHandler := http.HandlerFunc(func(mw http.ResponseWriter, mreq *http.Request) {
		mreq.URL.Scheme = "https"
		mreq.URL.Host = dest
		handleHTTP(mw, mreq, ctx)
	})

	go func() {
		server := &http.Server{Handler: mitmHandler}
		// Serve standard HTTP requests running inside the decrypted TCP stream
		server.Serve(&singleConnListener{conn: tlsConn})
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
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

func cloneHeaders(h http.Header) http.Header {
	c := make(http.Header, len(h))
	for k, v := range h {
		c[k] = append([]string(nil), v...)
	}
	return c
}
