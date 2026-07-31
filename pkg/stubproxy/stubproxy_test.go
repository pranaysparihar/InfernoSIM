package stubproxy

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	pb "infernosim/examples/grpcapp/echo"
	"infernosim/pkg/capture"
	"infernosim/pkg/event"
	"infernosim/pkg/grpcsim"
	"infernosim/pkg/matcher"
	"infernosim/pkg/scenario"
	"infernosim/pkg/simtemplate"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/proto"
)

func writeOutboundFixture(t *testing.T, events ...event.Event) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "outbound.log")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	enc := json.NewEncoder(f)
	for _, evt := range events {
		if err := enc.Encode(evt); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestStubReplaysCapturedResponse(t *testing.T) {
	path := writeOutboundFixture(t, event.Event{
		Type:               "OutboundCall",
		Method:             http.MethodGet,
		URL:                "http://dependency.test/api/value",
		Status:             http.StatusAccepted,
		ResponseCaptured:   true,
		ResponseHeaders:    http.Header{"Content-Type": {"application/json"}, "X-Contract": {"v1"}},
		ResponseBodyB64:    base64.StdEncoding.EncodeToString([]byte(`{"value":42}`)),
		ResponseBodySha256: "unused-by-stub",
	})
	stub, err := New(path, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	stub.ConfigureReplayCardinality(false, 1)

	req := httptest.NewRequest(http.MethodGet, "http://dependency.test/api/value", nil)
	rec := httptest.NewRecorder()
	stub.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d", rec.Code)
	}
	if rec.Header().Get("X-Contract") != "v1" || rec.Body.String() != `{"value":42}` {
		t.Fatalf("response headers/body not replayed: headers=%v body=%q", rec.Header(), rec.Body.String())
	}
}

func TestStubFanoutMatchesUnorderedCalls(t *testing.T) {
	path := writeOutboundFixture(t,
		event.Event{Type: "OutboundCall", Method: http.MethodGet, URL: "http://dependency.test/a", Status: 200},
		event.Event{Type: "OutboundCall", Method: http.MethodGet, URL: "http://dependency.test/b", Status: 204},
	)
	stub, err := New(path, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	stub.ConfigureReplayCardinality(true, 20)

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		for _, path := range []string{"/a", "/b"} {
			wg.Add(1)
			go func(path string) {
				defer wg.Done()
				req := httptest.NewRequest(http.MethodGet, "http://dependency.test"+path, nil)
				rec := httptest.NewRecorder()
				stub.ServeHTTP(rec, req)
				if rec.Code >= 400 {
					t.Errorf("%s returned %d", path, rec.Code)
				}
			}(path)
		}
	}
	wg.Wait()

	if got := stub.DivergenceReasons(); len(got) != 0 {
		t.Fatalf("unexpected divergences: %v", got)
	}
	if stub.ObservedCount() != 20 {
		t.Fatalf("observed = %d", stub.ObservedCount())
	}
}

func TestStubAllowsMissingOutboundLog(t *testing.T) {
	stub, err := New(filepath.Join(t.TempDir(), "missing.log"), "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if stub.ExpectedCount() != 0 {
		t.Fatalf("expected count = %d", stub.ExpectedCount())
	}
}

func TestNativeHTTPSResponseStubbing(t *testing.T) {
	ca, err := capture.NewCAStoreAt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ca.AllowedHosts = []string{"dependency.test"}
	path := writeOutboundFixture(t, event.Event{
		Type:             "OutboundCall",
		Method:           http.MethodGet,
		URL:              "https://dependency.test/api/value",
		Status:           http.StatusOK,
		ResponseCaptured: true,
		ResponseHeaders:  http.Header{"Content-Type": {"application/json"}},
		ResponseBodyB64:  base64.StdEncoding.EncodeToString([]byte(`{"secure":true}`)),
	})
	stub, err := NewWithOptions(path, "", nil, Options{TLSCA: ca})
	if err != nil {
		t.Fatal(err)
	}
	stub.ConfigureReplayCardinality(false, 1)
	proxy := httptest.NewServer(stub)
	defer proxy.Close()
	proxyURL, _ := url.Parse(proxy.URL)

	rootPEM, err := os.ReadFile(ca.CertificatePath())
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(rootPEM) {
		t.Fatal("could not add InfernoSIM CA")
	}
	client := &http.Client{Transport: &http.Transport{
		Proxy:           http.ProxyURL(proxyURL),
		TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12},
	}}
	response, err := client.Get("https://dependency.test/api/value")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusOK || string(body) != `{"secure":true}` {
		t.Fatalf("status=%d body=%q", response.StatusCode, body)
	}
}

func TestStubScenarioTakesPriorityAndTransitions(t *testing.T) {
	stub, err := NewWithOptions(filepath.Join(t.TempDir(), "missing.log"), "", nil, Options{
		Scenarios: []scenario.Config{{
			Name: "session", InitialState: "new",
			Steps: []scenario.Step{
				{
					Name: "start", State: "new", NextState: "ready",
					Match:    matcher.Rule{Methods: []string{"POST"}, PathRegex: `^/start$`},
					Response: scenario.Response{Status: 202},
				},
				{
					Name: "read", State: "ready",
					Match:    matcher.Rule{Methods: []string{"GET"}, PathRegex: `^/value$`},
					Response: scenario.Response{Status: 200, Body: "ready"},
				},
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	first := httptest.NewRecorder()
	stub.ServeHTTP(first, httptest.NewRequest("POST", "http://dependency.test/start", nil))
	if first.Code != 202 {
		t.Fatalf("start status=%d", first.Code)
	}
	second := httptest.NewRecorder()
	stub.ServeHTTP(second, httptest.NewRequest("GET", "http://dependency.test/value", nil))
	if second.Code != 200 || second.Body.String() != "ready" {
		t.Fatalf("read status=%d body=%q", second.Code, second.Body.String())
	}
}

func TestScenarioRendersDeterministicDynamicResponse(t *testing.T) {
	stub, err := NewWithOptions(filepath.Join(t.TempDir(), "missing.log"), "", nil, Options{
		Templates: simtemplate.Config{Seed: "test-seed"},
		Scenarios: []scenario.Config{{
			Name: "orders", InitialState: "ready",
			Steps: []scenario.Step{{
				Name: "create", State: "ready",
				Match: matcher.Rule{Methods: []string{http.MethodPost}, PathRegex: `^/orders$`},
				Response: scenario.Response{
					Status:       http.StatusCreated,
					Headers:      map[string][]string{"X-Order": {`{{ jsonPath "$.customer_id" }}`}},
					BodyTemplate: `{"id":"{{ uuid "order" }}","customer_id":"{{ jsonPath "$.customer_id" }}","created_at":"{{ now }}"}`,
				},
			}},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := func() *http.Request {
		req := httptest.NewRequest(http.MethodPost, "http://dependency.test/orders", strings.NewReader(`{"customer_id":"cust-7"}`))
		req.Header.Set("Content-Type", "application/json")
		return req
	}
	first := httptest.NewRecorder()
	stub.ServeHTTP(first, request())
	second := httptest.NewRecorder()
	stub.ServeHTTP(second, request())
	if first.Code != http.StatusCreated || first.Body.String() != second.Body.String() {
		t.Fatalf("first=%d %q second=%d %q", first.Code, first.Body.String(), second.Code, second.Body.String())
	}
	if first.Header().Get("X-Order") != "cust-7" || !strings.Contains(first.Body.String(), `"customer_id":"cust-7"`) {
		t.Fatalf("headers=%v body=%s", first.Header(), first.Body.String())
	}
}

func TestScenarioSynthesizesDescriptorAwareGRPCResponse(t *testing.T) {
	grpcConfig := grpcsim.Config{
		ProtoFiles:  []string{filepath.Join("..", "..", "examples", "grpcapp", "echo", "echo.proto")},
		ImportPaths: []string{filepath.Join("..", "..", "examples", "grpcapp", "echo")},
	}
	stub, err := NewWithOptions(filepath.Join(t.TempDir(), "missing.log"), "", nil, Options{
		Matching: matcher.Config{GRPC: grpcConfig},
		Scenarios: []scenario.Config{{
			Name: "grpc-echo", InitialState: "ready",
			Steps: []scenario.Step{{
				Name: "echo", State: "ready",
				Match: matcher.Rule{
					Methods:            []string{http.MethodPost},
					GRPCMethod:         "/echo.EchoService/Echo",
					ProtobufFieldRegex: map[string]string{"$.message": `^hello`},
				},
				Response: scenario.Response{
					Status:       http.StatusOK,
					GRPCStatus:   "OK",
					ProtobufJSON: `{"message":"{{ proto "$.message" }} virtualized"}`,
				},
			}},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(stub.Handler())
	defer server.Close()
	connection, err := grpc.NewClient(
		strings.TrimPrefix(server.URL, "http://"),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	response, err := pb.NewEchoServiceClient(connection).Echo(ctx, &pb.EchoRequest{Message: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if response.Message != "hello virtualized" {
		t.Fatalf("message=%q", response.Message)
	}
}

func TestScenarioSynthesizesStreamingGRPCResponses(t *testing.T) {
	grpcConfig := grpcsim.Config{
		ProtoFiles:  []string{filepath.Join("..", "..", "examples", "grpcapp", "echo", "echo.proto")},
		ImportPaths: []string{filepath.Join("..", "..", "examples", "grpcapp", "echo")},
	}
	steps := []scenario.Step{
		{
			Name: "server-stream", State: "ready",
			Match: matcher.Rule{Methods: []string{http.MethodPost}, GRPCMethod: "/echo.EchoService/ServerStream"},
			Response: scenario.Response{
				Status:         http.StatusOK,
				GRPCStatus:     "OK",
				ProtobufStream: []string{`{"message":"one"}`, `{"message":"two"}`},
			},
		},
		{
			Name: "client-stream", State: "ready",
			Match: matcher.Rule{
				Methods:            []string{http.MethodPost},
				GRPCMethod:         "/echo.EchoService/ClientStream",
				ProtobufFieldRegex: map[string]string{"$[1].message": `^two$`},
			},
			Response: scenario.Response{
				Status:       http.StatusOK,
				GRPCStatus:   "OK",
				ProtobufJSON: `{"message":"client stream accepted"}`,
			},
		},
		{
			Name: "bidi-stream", State: "ready",
			Match: matcher.Rule{
				Methods:            []string{http.MethodPost},
				GRPCMethod:         "/echo.EchoService/BidiStream",
				ProtobufFieldRegex: map[string]string{"$[0].message": `^alpha$`},
			},
			Response: scenario.Response{
				Status:         http.StatusOK,
				GRPCStatus:     "OK",
				ProtobufStream: []string{`{"message":"{{ proto "$[0].message" }}"}`, `{"message":"{{ proto "$[1].message" }}"}`},
			},
		},
	}
	stub, err := NewWithOptions(filepath.Join(t.TempDir(), "missing.log"), "", nil, Options{
		Matching:  matcher.Config{GRPC: grpcConfig},
		Scenarios: []scenario.Config{{Name: "streams", InitialState: "ready", Steps: steps}},
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(stub.Handler())
	defer server.Close()
	connection, err := grpc.NewClient(
		strings.TrimPrefix(server.URL, "http://"),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	client := pb.NewEchoServiceClient(connection)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	serverStream, err := client.ServerStream(ctx, &pb.EchoRequest{Message: "start"})
	if err != nil {
		t.Fatal(err)
	}
	for index, expected := range []string{"one", "two"} {
		response, recvErr := serverStream.Recv()
		if recvErr != nil || response.Message != expected {
			t.Fatalf("server stream[%d]=%#v err=%v", index, response, recvErr)
		}
	}
	if _, err := serverStream.Recv(); err != io.EOF {
		t.Fatalf("server stream end=%v", err)
	}

	clientStream, err := client.ClientStream(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := clientStream.Send(&pb.EchoRequest{Message: "one"}); err != nil {
		t.Fatal(err)
	}
	if err := clientStream.Send(&pb.EchoRequest{Message: "two"}); err != nil {
		t.Fatal(err)
	}
	clientResponse, err := clientStream.CloseAndRecv()
	if err != nil || clientResponse.Message != "client stream accepted" {
		t.Fatalf("client stream=%#v err=%v", clientResponse, err)
	}

	bidi, err := client.BidiStream(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := bidi.Send(&pb.EchoRequest{Message: "alpha"}); err != nil {
		t.Fatal(err)
	}
	if err := bidi.Send(&pb.EchoRequest{Message: "beta"}); err != nil {
		t.Fatal(err)
	}
	if err := bidi.CloseSend(); err != nil {
		t.Fatal(err)
	}
	for index, expected := range []string{"alpha", "beta"} {
		response, recvErr := bidi.Recv()
		if recvErr != nil || response.Message != expected {
			t.Fatalf("bidi[%d]=%#v err=%v", index, response, recvErr)
		}
	}
	if _, err := bidi.Recv(); err != io.EOF {
		t.Fatalf("bidi end=%v", err)
	}
}

func TestNativeHTTPSGRPCResponseVirtualization(t *testing.T) {
	ca, err := capture.NewCAStoreAt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ca.AllowedHosts = []string{"dependency.test"}
	message, err := proto.Marshal(&pb.EchoResponse{Message: "virtualized over h2"})
	if err != nil {
		t.Fatal(err)
	}
	frame := make([]byte, 5+len(message))
	binary.BigEndian.PutUint32(frame[1:5], uint32(len(message)))
	copy(frame[5:], message)

	path := writeOutboundFixture(t, event.Event{
		Type:             "OutboundCall",
		Method:           http.MethodPost,
		URL:              "https://dependency.test/echo.EchoService/Echo",
		Status:           http.StatusOK,
		ResponseCaptured: true,
		ResponseHeaders: http.Header{
			"Content-Type": {"application/grpc"},
		},
		ResponseTrailers:  http.Header{"Grpc-Status": {"0"}},
		ResponseBodyB64:   base64.StdEncoding.EncodeToString(frame),
		GrpcServiceMethod: "/echo.EchoService/Echo",
		GrpcStatus:        "0",
	})
	stub, err := NewWithOptions(path, "", nil, Options{TLSCA: ca})
	if err != nil {
		t.Fatal(err)
	}
	stub.ConfigureReplayCardinality(false, 1)
	proxy := httptest.NewServer(stub.Handler())
	defer proxy.Close()
	proxyURL, _ := url.Parse(proxy.URL)

	rootPEM, err := os.ReadFile(ca.CertificatePath())
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(rootPEM) {
		t.Fatal("could not add InfernoSIM CA")
	}
	dialer := func(ctx context.Context, _ string) (net.Conn, error) {
		conn, dialErr := (&net.Dialer{}).DialContext(ctx, "tcp", proxyURL.Host)
		if dialErr != nil {
			return nil, dialErr
		}
		if _, writeErr := fmt.Fprintf(
			conn,
			"CONNECT dependency.test:443 HTTP/1.1\r\nHost: dependency.test:443\r\n\r\n",
		); writeErr != nil {
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
			return nil, fmt.Errorf("proxy CONNECT returned %s", response.Status)
		}
		return &bufferedConn{Conn: conn, reader: reader}, nil
	}
	connection, err := grpc.NewClient(
		"passthrough:///dependency.test:443",
		grpc.WithContextDialer(dialer),
		grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{
			RootCAs:    roots,
			ServerName: "dependency.test",
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
	response, err := pb.NewEchoServiceClient(connection).Echo(ctx, &pb.EchoRequest{Message: "ignored"})
	if err != nil {
		t.Fatal(err)
	}
	if response.GetMessage() != "virtualized over h2" {
		t.Fatalf("response=%q", response.GetMessage())
	}
}

type bufferedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *bufferedConn) Read(buffer []byte) (int, error) {
	return c.reader.Read(buffer)
}
