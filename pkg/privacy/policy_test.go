package privacy

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
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
