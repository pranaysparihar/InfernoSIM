package capture

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"infernosim/pkg/event"
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
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("log permissions = %v", info.Mode().Perm())
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
