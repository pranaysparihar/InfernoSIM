package matcher

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	pb "infernosim/examples/grpcapp/echo"
	"infernosim/pkg/event"
	"infernosim/pkg/grpcsim"

	"google.golang.org/protobuf/proto"
)

func TestSemanticMatcherRegexHeadersJSONPathAndIgnoredFields(t *testing.T) {
	m, err := New(Config{
		IgnoredQueryParameters: []string{"timestamp"},
		IgnoredHeaders:         []string{"X-Request-ID"},
		Rules: []Rule{{
			Name:          "payment",
			Methods:       []string{http.MethodPost},
			HostRegex:     `^payments\.test$`,
			PathRegex:     `^/v1/payments/[0-9]+$`,
			HeaderRegex:   map[string]string{"X-Tenant": `^acme-[a-z]+$`},
			JSONPathRegex: map[string]string{"$.customer.id": `^cust_[0-9]+$`},
			IgnoredJSONPaths: []string{
				"$.request_id",
			},
			CompareJSON:    true,
			CompareHeaders: true,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	capturedBody := `{"customer":{"id":"cust_42"},"amount":10,"request_id":"old"}`
	captured := event.Event{
		Method:  http.MethodPost,
		URL:     "https://payments.test/v1/payments/100?mode=fast&timestamp=old",
		BodyB64: base64.StdEncoding.EncodeToString([]byte(capturedBody)),
		Headers: http.Header{
			"X-Tenant":     {"acme-east"},
			"X-Request-Id": {"old"},
		},
	}
	req := httptest.NewRequest(
		http.MethodPost,
		"https://payments.test/v1/payments/999?timestamp=new&mode=fast",
		strings.NewReader(`{"request_id":"new","amount":10,"customer":{"id":"cust_42"}}`),
	)
	req.Header.Set("X-Tenant", "acme-east")
	req.Header.Set("X-Request-ID", "new")
	body := []byte(`{"request_id":"new","amount":10,"customer":{"id":"cust_42"}}`)

	ok, reason := m.Match(captured, req, body)
	if !ok {
		t.Fatalf("expected semantic match, reason=%s", reason)
	}

	req.Header.Set("X-Tenant", "other")
	if ok, _ := m.Match(captured, req, body); ok {
		t.Fatal("expected header regex mismatch")
	}
}

func TestMatcherRejectsInvalidRegex(t *testing.T) {
	_, err := New(Config{Rules: []Rule{{PathRegex: "["}}})
	if err == nil {
		t.Fatal("expected invalid regex error")
	}
}

func TestJSONPathValueArray(t *testing.T) {
	root := map[string]any{"orders": []any{map[string]any{"id": "order-1"}}}
	got, ok := JSONPathValue(root, "$.orders[0].id")
	if !ok || got != "order-1" {
		t.Fatalf("got %v, %t", got, ok)
	}
	stream := []any{map[string]any{"id": "first"}, map[string]any{"id": "second"}}
	if got, ok := JSONPathValue(stream, "$[1].id"); !ok || got != "second" {
		t.Fatalf("root array got %v, %t", got, ok)
	}
}

func TestDescriptorAwareProtobufMatching(t *testing.T) {
	grpcConfig := grpcsim.Config{
		ProtoFiles:  []string{filepath.Join("..", "..", "examples", "grpcapp", "echo", "echo.proto")},
		ImportPaths: []string{filepath.Join("..", "..", "examples", "grpcapp", "echo")},
	}
	m, err := New(Config{
		GRPC: grpcConfig,
		Rules: []Rule{{
			Name:                  "echo",
			Methods:               []string{http.MethodPost},
			GRPCMethod:            "/echo.EchoService/Echo",
			ProtobufFieldRegex:    map[string]string{"$.message": `^hello`},
			IgnoredProtobufFields: []string{"$.message"},
			CompareProtobuf:       true,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	capturedFrame := grpcRequestFrame(t, &pb.EchoRequest{Message: "hello captured"})
	runtimeFrame := grpcRequestFrame(t, &pb.EchoRequest{Message: "hello runtime"})
	captured := event.Event{
		Method:  http.MethodPost,
		URL:     "https://dependency.test/echo.EchoService/Echo",
		BodyB64: base64.StdEncoding.EncodeToString(capturedFrame),
	}
	request := httptest.NewRequest(http.MethodPost, captured.URL, strings.NewReader(string(runtimeFrame)))
	request.Header.Set("Content-Type", "application/grpc")
	if matched, reason := m.Match(captured, request, runtimeFrame); !matched {
		t.Fatalf("expected semantic Protobuf match: %s", reason)
	}
	mismatch := grpcRequestFrame(t, &pb.EchoRequest{Message: "goodbye"})
	if matched, _ := m.Match(captured, request, mismatch); matched {
		t.Fatal("expected Protobuf field regex mismatch")
	}
}

func grpcRequestFrame(t *testing.T, message proto.Message) []byte {
	t.Helper()
	payload, err := proto.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	frame := make([]byte, 5+len(payload))
	binary.BigEndian.PutUint32(frame[1:5], uint32(len(payload)))
	copy(frame[5:], payload)
	return frame
}

func FuzzJSONPathValue(f *testing.F) {
	f.Add(`{"a":[{"b":"value"}]}`, "$.a[0].b")
	f.Fuzz(func(t *testing.T, document, path string) {
		var value any
		if json.Unmarshal([]byte(document), &value) != nil {
			return
		}
		_, _ = JSONPathValue(value, path)
	})
}

func BenchmarkSemanticMatch(b *testing.B) {
	m, _ := New(Config{Rules: []Rule{{
		Methods:       []string{http.MethodPost},
		PathRegex:     `^/orders/[0-9]+$`,
		JSONPathRegex: map[string]string{"$.id": `^[0-9]+$`},
	}}})
	body := []byte(`{"id":42}`)
	captured := event.Event{Method: http.MethodPost, URL: "https://orders.test/orders/1"}
	request := httptest.NewRequest(http.MethodPost, "https://orders.test/orders/2", strings.NewReader(string(body)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m.Match(captured, request, body)
	}
}
