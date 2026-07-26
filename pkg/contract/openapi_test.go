package contract

import (
	"encoding/base64"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"infernosim/pkg/event"
)

func writeSpec(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "openapi.yaml")
	spec := `openapi: 3.1.0
info:
  title: Test API
  version: 1.0.0
paths:
  /users/{id}:
    post:
      requestBody:
        required: true
        content:
          application/json:
            schema:
              type: object
              required: [name]
              properties:
                name:
                  type: string
      responses:
        "200":
          description: user
          headers:
            X-Contract:
              required: true
              schema:
                type: string
          content:
            application/json:
              schema:
                $ref: "#/components/schemas/User"
components:
  schemas:
    User:
      type: object
      required: [id, name]
      properties:
        id:
          type: integer
        name:
          type: string
`
	if err := os.WriteFile(path, []byte(spec), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestOpenAPIValidatesPathTemplateRequestAndResponse(t *testing.T) {
	validator, err := Load(writeSpec(t))
	if err != nil {
		t.Fatal(err)
	}
	events := []event.Event{{
		Method:          http.MethodPost,
		URL:             "https://api.test/users/42",
		Status:          200,
		Headers:         http.Header{"Content-Type": {"application/json"}},
		BodyB64:         base64.StdEncoding.EncodeToString([]byte(`{"name":"Ada"}`)),
		ResponseHeaders: http.Header{"Content-Type": {"application/json"}, "X-Contract": {"v1"}},
		ResponseBodyB64: base64.StdEncoding.EncodeToString([]byte(`{"id":42,"name":"Ada"}`)),
	}}
	if findings := validator.ValidateEvents(events, "baseline"); len(findings) != 0 {
		t.Fatalf("unexpected findings: %#v", findings)
	}
}

func TestOpenAPIReportsSchemaAndStatusViolations(t *testing.T) {
	validator, err := Load(writeSpec(t))
	if err != nil {
		t.Fatal(err)
	}
	events := []event.Event{{
		Method:          http.MethodPost,
		URL:             "https://api.test/users/42",
		Status:          201,
		Headers:         http.Header{"Content-Type": {"application/json"}},
		BodyB64:         base64.StdEncoding.EncodeToString([]byte(`{}`)),
		ResponseHeaders: http.Header{"Content-Type": {"application/json"}},
		ResponseBodyB64: base64.StdEncoding.EncodeToString([]byte(`{"id":"wrong"}`)),
	}}
	findings := validator.ValidateEvents(events, "candidate")
	if len(findings) < 2 {
		t.Fatalf("expected request and status findings, got %#v", findings)
	}
}

func TestDriftFindings(t *testing.T) {
	baseline := []event.Event{{Method: "GET", URL: "/value", Status: 200, ResponseHeaders: http.Header{"Content-Type": {"application/json"}}}}
	candidate := []event.Event{{Method: "GET", URL: "/value", Status: 500, ResponseHeaders: http.Header{"Content-Type": {"text/plain"}}}}
	if findings := DriftFindings(baseline, candidate); len(findings) != 2 {
		t.Fatalf("findings=%#v", findings)
	}
}

func TestExampleOpenAPIIsValid(t *testing.T) {
	if _, err := Load(filepath.Join("..", "..", "examples", "openapi.yaml")); err != nil {
		t.Fatal(err)
	}
}

func TestOperationPrefersExactPathAndStatusRange(t *testing.T) {
	exact := &Operation{OperationID: "exact"}
	template := &Operation{OperationID: "template"}
	validator := &Validator{document: Document{Paths: map[string]PathItem{
		"/users/{id}": {Get: template},
		"/users/me":   {Get: exact},
	}}}
	path, operation := validator.operation(http.MethodGet, "/users/me")
	if path != "/users/me" || operation != exact {
		t.Fatalf("selected path=%q operation=%#v", path, operation)
	}
	responses := map[string]Response{"2XX": {Description: "success"}}
	if response, ok := responseForStatus(responses, 204); !ok || response.Description != "success" {
		t.Fatalf("status range did not match: %#v %t", response, ok)
	}
}
