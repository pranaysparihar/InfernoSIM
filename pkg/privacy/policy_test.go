package privacy

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPolicyRedactsAndTokenizesDeterministically(t *testing.T) {
	t.Setenv("INFERNOSIM_TEST_TOKEN_KEY", "0123456789abcdef0123456789abcdef")
	path := filepath.Join(t.TempDir(), "privacy.yaml")
	data := []byte(`version: 1
capture_bodies: true
token_key_env: INFERNOSIM_TEST_TOKEN_KEY
headers:
  - name: Authorization
    action: redact
  - name: X-Customer-ID
    action: tokenize
query_parameters:
  - name: api_key
    action: drop
json_fields:
  - path: $.customer.email
    action: tokenize
  - path: $.card.number
    action: redact
`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	policy, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	headers := http.Header{
		"Authorization": {"Bearer secret"},
		"X-Customer-Id": {"customer-1"},
	}
	transformedHeaders := policy.ApplyHeaders(headers)
	if transformedHeaders.Get("Authorization") != "[REDACTED]" {
		t.Fatal("authorization was not redacted")
	}
	token := transformedHeaders.Get("X-Customer-ID")
	if token == "" || token == "customer-1" {
		t.Fatalf("customer ID was not tokenized: %q", token)
	}
	if policy.ApplyHeaders(headers).Get("X-Customer-ID") != token {
		t.Fatal("tokenization is not deterministic")
	}

	inputURL, _ := url.Parse("https://example.test/path?api_key=secret&mode=fast")
	if got := policy.ApplyURL(inputURL).Query().Get("api_key"); got != "" {
		t.Fatalf("api_key not dropped: %q", got)
	}
	body, err := policy.ApplyBody([]byte(`{"customer":{"email":"a@example.test"},"card":{"number":"4111"}}`))
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatal(err)
	}
	customer := decoded["customer"].(map[string]any)
	card := decoded["card"].(map[string]any)
	if customer["email"] == "a@example.test" || card["number"] != "[REDACTED]" {
		t.Fatalf("body policy not applied: %s", body)
	}
}

func TestPolicyRequiresKeyForTokenization(t *testing.T) {
	path := filepath.Join(t.TempDir(), "privacy.yaml")
	if err := os.WriteFile(path, []byte(`version: 1
headers:
  - name: X-ID
    action: tokenize
`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected missing token key error")
	}
}

func TestExamplePolicyIsValid(t *testing.T) {
	t.Setenv("INFERNOSIM_TOKEN_KEY", "0123456789abcdef0123456789abcdef")
	if _, err := Load(filepath.Join("..", "..", "examples", "privacy-policy.yaml")); err != nil {
		t.Fatal(err)
	}
}

func TestApplyMessageRedactsKeyHeadersAndPayload(t *testing.T) {
	t.Setenv("INFERNO_MESSAGE_KEY", "0123456789abcdef0123456789abcdef")
	path := filepath.Join(t.TempDir(), "privacy.yaml")
	policyYAML := `version: 1
capture_bodies: true
token_key_env: INFERNO_MESSAGE_KEY
message_key: tokenize
message_headers:
  - name: authorization
    action: drop
  - name: x-user-email
    action: redact
json_fields:
  - path: $.customer.email
    action: tokenize
`
	if err := os.WriteFile(path, []byte(policyYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	policy, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	key, headers, payload, err := policy.ApplyMessage(
		[]byte("customer-42"),
		[]MessageHeader{{Name: "Authorization", Value: []byte("Bearer secret")}, {Name: "X-User-Email", Value: []byte("person@example.com")}},
		[]byte(`{"customer":{"email":"person@example.com"}}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(key), "tok_") {
		t.Fatalf("key=%q", key)
	}
	if len(headers) != 1 || headers[0].Name != "X-User-Email" || string(headers[0].Value) != "[REDACTED]" {
		t.Fatalf("headers=%v", headers)
	}
	if strings.Contains(string(payload), "person@example.com") || !strings.Contains(string(payload), "tok_") {
		t.Fatalf("payload=%s", payload)
	}
}

func TestPolicyRejectsDuplicateRules(t *testing.T) {
	path := filepath.Join(t.TempDir(), "privacy.yaml")
	policy := `version: 1
capture_bodies: true
message_headers:
  - name: Authorization
    action: redact
  - name: authorization
    action: drop
`
	if err := os.WriteFile(path, []byte(policy), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected duplicate message header rejection")
	}
}
