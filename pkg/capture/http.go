package capture

import (
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"infernosim/pkg/event"
)

// StartInboundProxy starts a reverse proxy that listens on listenAddr and forwards to targetURL.
// It returns the *http.Server so the caller can shut it down if needed.
func StartInboundProxy(listenAddr string, targetURL *url.URL, logger *event.Logger) (*http.Server, error) {
    // Create a reverse proxy handler pointing to the target service
    proxy := httputil.NewSingleHostReverseProxy(targetURL)
    // We want to capture the response as well, so add a modify function
    proxy.ModifyResponse = func(resp *http.Response) error {
        // Log the outbound response (service -> client) when received
        statusCode := resp.StatusCode
        req := resp.Request   // original request that led to this response
        // We use a custom header to retrieve a correlation ID if we set one
        corrID := req.Header.Get("X-Inferno-TraceID")
        if corrID == "" {
            corrID = event.GenerateID()
        }
        evt := &event.Event{
            ID:        event.GenerateID(),
            Type:      "InboundResponse",
            Timestamp: time.Now().UTC(),
            Service:   targetURL.Host, // service host:port as identifier
            Method:    req.Method,
            URL:       req.URL.String(),
            Status:    statusCode,
            TraceID:   corrID,
            // We could capture response headers or body if needed (omitted for brevity)
        }
        logger.Write(evt)
        log.Printf("Logged response for inbound request %s -> %d", req.URL.Path, statusCode)
        return nil
    }
    // Wrap the director to inject a trace ID for correlation
    originalDirector := proxy.Director
    proxy.Director = func(req *http.Request) {
        originalDirector(req)
        // Inject a trace header to propagate trace context to service, if not already present
        if req.Header.Get("X-Inferno-TraceID") == "" {
            traceID := event.GenerateID()
            req.Header.Set("X-Inferno-TraceID", traceID)
            // Also store this ID in context (if needed) or on a map for correlation - omitted for simplicity
        }
        // Log the inbound request event
        evt := &event.Event{
            ID:        event.GenerateID(),
            Type:      "InboundRequest",
            Timestamp: time.Now().UTC(),
            Service:   targetURL.Host,
            Method:    req.Method,
            URL:       req.URL.String(),
            // We log headers and body size for debugging; full body capture can be added if needed
            Headers:   cloneHeaders(req.Header),
            BodySize:  req.ContentLength,
            TraceID:   req.Header.Get("X-Inferno-TraceID"),
        }
        logger.Write(evt)
        log.Printf("Logged inbound request %s %s", req.Method, req.URL.Path)
    }

    // Create HTTP server
    server := &http.Server{
        Addr:    listenAddr,
        Handler: proxy,
    }
    // Start server in a new goroutine (non-blocking)
    go func() {
        if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
            log.Fatalf("Inbound proxy server error: %v", err)
        }
    }()
    return server, nil
}

// StartForwardProxy starts a forward (outbound) proxy on listenAddr.
// It captures any HTTP requests (or CONNECT for HTTPS) coming from a service.
func StartForwardProxy(listenAddr string, logger *event.Logger) (*http.Server, error) {
    // Custom handler for proxy behavior
    handler := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
        if req.Method == http.MethodConnect {
            // Handle HTTPS tunneling
            handleConnect(w, req, logger)
        } else {
            // Handle plain HTTP requests via proxy
            handleHTTP(w, req, logger)
        }
    })
    server := &http.Server{
        Addr:    listenAddr,
        Handler: handler,
    }
    go func() {
        if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
            log.Fatalf("Forward proxy server error: %v", err)
        }
    }()
    return server, nil
}

// handleHTTP handles non-CONNECT requests (http://) for the forward proxy.
func handleHTTP(w http.ResponseWriter, req *http.Request, logger *event.Logger) {
    startTime := time.Now().UTC()
    // We have a full URL in req.URL when using proxy. We use http.DefaultTransport to fetch it.
    outReq, err := http.NewRequest(req.Method, req.URL.String(), req.Body)
    if err != nil {
        http.Error(w, "Bad Gateway", http.StatusBadGateway)
        return
    }
    outReq.Header = cloneHeaders(req.Header)
    // Remove Proxy-specific headers that should not be forwarded
    outReq.Header.Del("Proxy-Connection")
    outReq.Header.Del("Proxy-Authenticate")
    outReq.Header.Del("Proxy-Authorization")
    // (We could add TraceID propagation here similar to inbound if needed)

    resp, err := http.DefaultTransport.RoundTrip(outReq)
    var statusCode int
    if err != nil {
        log.Printf("Error forwarding request to %s: %v", req.URL, err)
        statusCode = 0  // indicate no response
    } else {
        statusCode = resp.StatusCode
        // Write response back to the client
        copyResponse(w, resp)
        resp.Body.Close()
    }
    // Log the outbound call event and its response/outcome
    evt := &event.Event{
        ID:        event.GenerateID(),
        Type:      "OutboundCall",
        Timestamp: startTime,
        Service:   "", // could be filled with source service info if known
        Method:    req.Method,
        URL:       req.URL.String(),
        Headers:   cloneHeaders(req.Header),
        BodySize:  req.ContentLength,
        Status:    statusCode,
        Duration:  time.Since(startTime), // how long the call took
    }
    if err != nil {
        evt.Error = err.Error()
    }
    logger.Write(evt)
    log.Printf("Logged outbound call: %s %s -> %d", req.Method, req.URL, statusCode)
}

// handleConnect handles CONNECT method for HTTPS tunneling in the forward proxy.
func handleConnect(w http.ResponseWriter, req *http.Request, logger *event.Logger) {
    dest := req.Host // e.g. example.com:443
    log.Printf("Handling CONNECT to %s", dest)
    // Connect to the destination
    targetConn, err := net.DialTimeout("tcp", dest, 10*time.Second)
    if err != nil {
        http.Error(w, err.Error(), http.StatusServiceUnavailable)
        return
    }
    // Send success to client
    w.WriteHeader(http.StatusOK)
    hijacker, ok := w.(http.Hijacker)
    if !ok {
        http.Error(w, "Proxy error", http.StatusInternalServerError)
        targetConn.Close()
        return
    }
    clientConn, _, err := hijacker.Hijack()
    if err != nil {
        log.Println("Hijack error:", err)
        targetConn.Close()
        return
    }
    // Now we have raw clientConn and targetConn, pipe data between them
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
    // Log an event for the outbound connection (without full HTTP details since it's encrypted)
    evt := &event.Event{
        ID:        event.GenerateID(),
        Type:      "OutboundCall",
        Timestamp: time.Now().UTC(),
        Service:   "",
        Method:    "CONNECT",
        URL:       dest,  // host:port
        Status:    200,   // tunnel established
        // We can't log headers or body for TLS tunnels, but we note the event.
    }
    logger.Write(evt)
    log.Printf("Logged outbound CONNECT to %s", dest)
}

// helper: copyResponse writes the downstream response to the original ResponseWriter
func copyResponse(w http.ResponseWriter, resp *http.Response) {
    // Copy status
    w.WriteHeader(resp.StatusCode)
    // Copy headers
    for k, vals := range resp.Header {
        // Exclude hop-by-hop headers if any (not likely needed)
        if strings.ToLower(k) == "connection" {
            continue
        }
        for _, v := range vals {
            w.Header().Add(k, v)
        }
    }
    // Copy body
    io.Copy(w, resp.Body)
}

// helper: cloneHeaders makes a shallow copy of http.Header (to avoid side effects)
func cloneHeaders(h http.Header) http.Header {
	c := make(http.Header, len(h))
	for k, v := range h {
		c[k] = append([]string(nil), v...)
	}
	return c
}