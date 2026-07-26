package matcher

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"infernosim/pkg/event"
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
}
