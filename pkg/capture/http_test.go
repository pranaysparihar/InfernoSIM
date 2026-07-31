package capture

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	pb "infernosim/examples/grpcapp/echo"
	"infernosim/pkg/event"
	"infernosim/pkg/privacy"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

type countingReadCloser struct {
	reader *bytes.Reader
	read   int
}

func (r *countingReadCloser) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	r.read += n
	return n, err
}

func (r *countingReadCloser) Close() error { return nil }

func TestPeekBodyBoundsMemoryAndPreservesStream(t *testing.T) {
	original := bytes.Repeat([]byte("x"), maxBodySize*4)
	source := &countingReadCloser{reader: bytes.NewReader(original)}

	snapshot, truncated, restored, err := peekBody(source)
	if err != nil {
		t.Fatal(err)
	}
	if !truncated || len(snapshot) != maxBodySize {
		t.Fatalf("snapshot = %d bytes, truncated=%t", len(snapshot), truncated)
	}
	if source.read > maxBodySize+1 {
		t.Fatalf("peek read %d bytes before forwarding; expected at most %d", source.read, maxBodySize+1)
	}
	forwarded, err := io.ReadAll(restored)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(forwarded, original) {
		t.Fatal("forwarded stream differs from original body")
	}
}

func TestStartForwardProxyReportsBindFailure(t *testing.T) {
	first, err := StartForwardProxy("127.0.0.1:0", &ProxyContext{})
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()

	if _, err := StartForwardProxy(first.Addr, &ProxyContext{}); err == nil {
		t.Fatal("expected a synchronous bind error")
	}
}

func TestForwardProxyCapturesExchangeAndRedactsSecrets(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Set-Cookie", "session=upstream-secret")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"token":"response-secret"}`))
	}))
	defer upstream.Close()

	logPath := filepath.Join(t.TempDir(), "outbound.log")
	logger, err := event.NewLogger(logPath)
	if err != nil {
		t.Fatal(err)
	}
	defer logger.Close()

	proxy, err := StartForwardProxy("127.0.0.1:0", &ProxyContext{
		Logger:                   logger,
		AllowPrivateDestinations: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()

	proxyURL, _ := url.Parse("http://" + proxy.Addr)
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}}
	req, _ := http.NewRequest(http.MethodPost, upstream.URL+"/charge", bytes.NewBufferString(`{"card":"secret"}`))
	req.Header.Set("Authorization", "Bearer request-secret")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()

	f, err := os.Open(logPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var captured event.Event
	if err := json.NewDecoder(f).Decode(&captured); err != nil {
		t.Fatal(err)
	}
	if captured.Status != http.StatusCreated || !captured.ResponseCaptured {
		t.Fatalf("captured status=%d responseCaptured=%t", captured.Status, captured.ResponseCaptured)
	}
	if got := http.Header(captured.Headers).Get("Authorization"); got != "[REDACTED]" {
		t.Fatalf("Authorization = %q", got)
	}
	if captured.BodyB64 != "" || captured.ResponseBodyB64 != "" {
		t.Fatal("secure capture stored raw payload bytes")
	}
	if !captured.BodyRedacted || !captured.ResponseBodyRedacted {
		t.Fatal("redaction flags were not recorded")
	}
	if captured.BodySha256 == "" || captured.ResponseBodySha256 == "" {
		t.Fatal("redacted capture must retain payload fingerprints")
	}
	info, err := os.Stat(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("log permissions = %v", info.Mode().Perm())
	}
}

func TestForwardProxyAppliesPrivacyPolicyBeforeStorage(t *testing.T) {
	t.Setenv("INFERNOSIM_CAPTURE_TOKEN_KEY", "0123456789abcdef0123456789abcdef")
	policyPath := filepath.Join(t.TempDir(), "privacy.yaml")
	if err := os.WriteFile(policyPath, []byte(`version: 1
capture_bodies: true
token_key_env: INFERNOSIM_CAPTURE_TOKEN_KEY
headers:
  - name: X-Customer-ID
    action: tokenize
query_parameters:
  - name: secret
    action: redact
json_fields:
  - path: $.email
    action: tokenize
`), 0o600); err != nil {
		t.Fatal(err)
	}
	policy, err := privacy.Load(policyPath)
	if err != nil {
		t.Fatal(err)
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"email":"response@example.test"}`))
	}))
	defer upstream.Close()
	logPath := filepath.Join(t.TempDir(), "outbound.log")
	logger, err := event.NewLogger(logPath)
	if err != nil {
		t.Fatal(err)
	}
	defer logger.Close()
	proxy, err := StartForwardProxy("127.0.0.1:0", &ProxyContext{
		Logger:                   logger,
		AllowPrivateDestinations: true,
		Privacy:                  policy,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()
	proxyURL, _ := url.Parse("http://" + proxy.Addr)
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}}
	req, _ := http.NewRequest(http.MethodPost, upstream.URL+"/profile?secret=value", bytes.NewBufferString(`{"email":"request@example.test"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Customer-ID", "customer-123")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	file, err := os.Open(logPath)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	var captured event.Event
	if err := json.NewDecoder(file).Decode(&captured); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(captured.URL, "secret=value") || !strings.Contains(captured.URL, "%5BREDACTED%5D") {
		t.Fatalf("query policy not applied: %s", captured.URL)
	}
	if http.Header(captured.Headers).Get("X-Customer-ID") == "customer-123" {
		t.Fatal("header was not tokenized")
	}
	requestBody, _ := base64.StdEncoding.DecodeString(captured.BodyB64)
	responseBody, _ := base64.StdEncoding.DecodeString(captured.ResponseBodyB64)
	if strings.Contains(string(requestBody), "request@example.test") || strings.Contains(string(responseBody), "response@example.test") {
		t.Fatalf("stored body contains plaintext PII: request=%s response=%s", requestBody, responseBody)
	}
}

func TestConnectTunnelForwardsTLS(t *testing.T) {
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("tunnel-ok"))
	}))
	defer upstream.Close()
	proxy, err := StartForwardProxy("127.0.0.1:0", &ProxyContext{AllowPrivateDestinations: true})
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()

	proxyURL, _ := url.Parse("http://" + proxy.Addr)
	transport := upstream.Client().Transport.(*http.Transport).Clone()
	transport.Proxy = http.ProxyURL(proxyURL)
	client := &http.Client{Transport: transport}
	resp, err := client.Get(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "tunnel-ok" {
		t.Fatalf("body = %q", body)
	}
}

func TestForwardProxyBlocksPrivateHTTPByDefault(t *testing.T) {
	upstreamCalls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls++
	}))
	defer upstream.Close()
	proxy, err := StartForwardProxy("127.0.0.1:0", &ProxyContext{})
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()
	proxyURL, _ := url.Parse("http://" + proxy.Addr)
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}}
	resp, err := client.Get(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway && resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if upstreamCalls != 0 {
		t.Fatalf("private upstream received %d calls", upstreamCalls)
	}
}

func TestMITMDecryptsAllowlistedTLS(t *testing.T) {
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("mitm-ok"))
	}))
	defer upstream.Close()

	caDir := t.TempDir()
	ca := &CAStore{
		certPath:     filepath.Join(caDir, "ca.crt"),
		keyPath:      filepath.Join(caDir, "ca.key"),
		leafCert:     make(map[string]*tls.Certificate),
		AllowedHosts: []string{"127.0.0.1"},
	}
	if err := ca.generateCA(); err != nil {
		t.Fatal(err)
	}
	caPEM, err := os.ReadFile(ca.certPath)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caPEM) {
		t.Fatal("failed to trust generated CA")
	}

	logPath := filepath.Join(t.TempDir(), "mitm.log")
	logger, err := event.NewLogger(logPath)
	if err != nil {
		t.Fatal(err)
	}
	defer logger.Close()
	proxy, err := StartForwardProxy("127.0.0.1:0", &ProxyContext{
		Logger:                   logger,
		CA:                       ca,
		UseMITM:                  true,
		AllowPrivateDestinations: true,
		AllowInsecureUpstream:    true,
		CaptureSensitiveData:     true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()

	proxyURL, _ := url.Parse("http://" + proxy.Addr)
	client := &http.Client{Transport: &http.Transport{
		Proxy:           http.ProxyURL(proxyURL),
		TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12},
	}}
	resp, err := client.Get(upstream.URL + "/secure")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if string(body) != "mitm-ok" {
		t.Fatalf("body = %q", body)
	}

	f, err := os.Open(logPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	dec := json.NewDecoder(f)
	foundDecrypted := false
	for {
		var evt event.Event
		if err := dec.Decode(&evt); err != nil {
			break
		}
		if evt.Method == http.MethodGet && evt.URL != "" && evt.ResponseBodySha256 != "" {
			foundDecrypted = true
		}
	}
	if !foundDecrypted {
		t.Fatal("MITM capture did not log decrypted HTTP exchange")
	}
}

func TestMITMCapturesHTTPSGRPCResponseAndTrailers(t *testing.T) {
	ca, err := NewCAStoreAt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ca.AllowedHosts = []string{"127.0.0.1"}
	upstreamCert, err := ca.GenerateLeafCert("127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	grpcServer := grpc.NewServer(grpc.Creds(credentials.NewTLS(&tls.Config{
		Certificates: []tls.Certificate{*upstreamCert},
		MinVersion:   tls.VersionTLS12,
		NextProtos:   []string{"h2"},
	})))
	pb.RegisterEchoServiceServer(grpcServer, &captureTestEchoServer{})
	go func() {
		_ = grpcServer.Serve(listener)
	}()
	defer grpcServer.Stop()

	logPath := filepath.Join(t.TempDir(), "grpc-mitm.log")
	logger, err := event.NewLogger(logPath)
	if err != nil {
		t.Fatal(err)
	}
	defer logger.Close()
	proxy, err := StartForwardProxy("127.0.0.1:0", &ProxyContext{
		Logger:                   logger,
		CA:                       ca,
		UseMITM:                  true,
		AllowPrivateDestinations: true,
		AllowInsecureUpstream:    true,
		CaptureSensitiveData:     true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()

	rootPEM, err := os.ReadFile(ca.CertificatePath())
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(rootPEM) {
		t.Fatal("failed to trust generated CA")
	}
	target := listener.Addr().String()
	dialer := func(ctx context.Context, _ string) (net.Conn, error) {
		conn, dialErr := (&net.Dialer{}).DialContext(ctx, "tcp", proxy.Addr)
		if dialErr != nil {
			return nil, dialErr
		}
		if _, writeErr := fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", target, target); writeErr != nil {
			_ = conn.Close()
			return nil, writeErr
		}
		reader := bufio.NewReader(conn)
		response, readErr := http.ReadResponse(reader, &http.Request{Method: http.MethodConnect})
		if readErr != nil {
			_ = conn.Close()
			return nil, readErr
		}
		_ = response.Body.Close()
		if response.StatusCode != http.StatusOK {
			_ = conn.Close()
			return nil, fmt.Errorf("CONNECT returned %s", response.Status)
		}
		return &captureBufferedConn{Conn: conn, reader: reader}, nil
	}
	connection, err := grpc.NewClient(
		"passthrough:///"+target,
		grpc.WithContextDialer(dialer),
		grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{
			RootCAs:    roots,
			ServerName: "127.0.0.1",
			MinVersion: tls.VersionTLS12,
			NextProtos: []string{"h2"},
		})),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	response, err := pb.NewEchoServiceClient(connection).Echo(ctx, &pb.EchoRequest{Message: "capture"})
	if err != nil {
		t.Fatal(err)
	}
	if response.GetMessage() != "Echo: capture" {
		t.Fatalf("response=%q", response.GetMessage())
	}

	file, err := os.Open(logPath)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	found := false
	for {
		var captured event.Event
		if err := decoder.Decode(&captured); err != nil {
			break
		}
		if captured.Type == "OutboundCall" && captured.GrpcServiceMethod == "/echo.EchoService/Echo" {
			found = true
			if captured.GrpcStatus != "0" {
				t.Fatalf("grpc status=%q", captured.GrpcStatus)
			}
			if http.Header(captured.ResponseTrailers).Get("Grpc-Status") != "0" {
				t.Fatalf("trailers=%v", captured.ResponseTrailers)
			}
			if captured.ResponseBodyB64 == "" {
				t.Fatal("captured gRPC response body is empty")
			}
		}
	}
	if !found {
		t.Fatal("HTTPS gRPC exchange was not captured")
	}
}

type captureTestEchoServer struct {
	pb.UnimplementedEchoServiceServer
}

func (s *captureTestEchoServer) Echo(_ context.Context, request *pb.EchoRequest) (*pb.EchoResponse, error) {
	return &pb.EchoResponse{Message: "Echo: " + request.GetMessage()}, nil
}

type captureBufferedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *captureBufferedConn) Read(buffer []byte) (int, error) {
	return c.reader.Read(buffer)
}
