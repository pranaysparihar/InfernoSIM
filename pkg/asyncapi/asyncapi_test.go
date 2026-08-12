package asyncapi

import (
	"os"
	"path/filepath"
	"testing"

	"infernosim/pkg/message"
)

func TestValidateAsyncAPI3Messages(t *testing.T) {
	path := filepath.Join(t.TempDir(), "asyncapi.yaml")
	spec := `asyncapi: 3.0.0
channels:
  paymentAuthorized:
    address: payment.authorized
    messages:
      PaymentAuthorized:
        $ref: '#/components/messages/PaymentAuthorized'
components:
  messages:
    PaymentAuthorized:
      name: PaymentAuthorized
      contentType: application/json
      headers:
        type: object
        required: [event-version]
        properties:
          event-version:
            type: string
            enum: ['1']
      payload:
        $ref: '#/components/schemas/Payment'
  schemas:
    Payment:
      type: object
      additionalProperties: false
      required: [payment_id, status]
      properties:
        payment_id:
          type: string
          pattern: '^pay_[0-9]+$'
        status:
          type: string
          enum: [authorized]
`
	if err := os.WriteFile(path, []byte(spec), 0o600); err != nil {
		t.Fatal(err)
	}
	validator, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	valid := message.New("payment.authorized", nil, []byte(`{"payment_id":"pay_42","status":"authorized"}`), map[string][]byte{"event-version": []byte("1")})
	valid.Schema = "PaymentAuthorized"
	if findings := validator.Validate([]message.Record{valid}); len(findings) != 0 {
		t.Fatalf("valid message findings=%+v", findings)
	}
	invalid := message.New("payment.authorized", nil, []byte(`{"payment_id":"wrong","extra":true}`), map[string][]byte{"event-version": []byte("1")})
	findings := validator.Validate([]message.Record{invalid})
	if len(findings) != 1 || findings[0].RuleID != "ASYNCAPI_SCHEMA_VIOLATION" {
		t.Fatalf("invalid findings=%+v", findings)
	}
	invalidHeader := message.New("payment.authorized", nil, []byte(`{"payment_id":"pay_42","status":"authorized"}`), nil)
	findings = validator.Validate([]message.Record{invalidHeader})
	if len(findings) != 1 || findings[0].RuleID != "ASYNCAPI_HEADER_SCHEMA_VIOLATION" {
		t.Fatalf("header findings=%+v", findings)
	}
	unknown := message.New("other.topic", nil, []byte(`{}`), nil)
	findings = validator.Validate([]message.Record{unknown})
	if len(findings) != 1 || findings[0].RuleID != "ASYNCAPI_UNDOCUMENTED_CHANNEL" {
		t.Fatalf("unknown findings=%+v", findings)
	}
}

func TestLoadRejectsAsyncAPI2(t *testing.T) {
	path := filepath.Join(t.TempDir(), "asyncapi.yaml")
	_ = os.WriteFile(path, []byte("asyncapi: 2.6.0\nchannels:\n  topic: {}\n"), 0o600)
	if _, err := Load(path); err == nil {
		t.Fatal("expected version rejection")
	}
}

func TestLoadRejectsInvalidSupportedSchemaSubset(t *testing.T) {
	for name, schema := range map[string]string{
		"unknown type":  "type: mystery",
		"invalid regex": "type: string\n          pattern: '[invalid'",
		"missing ref":   "$ref: '#/components/schemas/Missing'",
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "asyncapi.yaml")
			spec := "asyncapi: 3.0.0\nchannels:\n  events:\n    address: events\n    messages:\n      Event:\n        payload:\n          " + schema + "\n"
			if err := os.WriteFile(path, []byte(spec), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(path); err == nil {
				t.Fatal("expected contract validation failure")
			}
		})
	}
	path := filepath.Join(t.TempDir(), "asyncapi.yaml")
	spec := "asyncapi: 3.0.0\nchannels:\n  events:\n    messages:\n      Event:\n        contentType: application/avro\n"
	if err := os.WriteFile(path, []byte(spec), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected non-JSON content type rejection")
	}
}

func TestValidateNullableUnion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "asyncapi.yaml")
	spec := `asyncapi: 3.0.0
channels:
  events:
    address: events
    messages:
      Event:
        payload:
          type: [string, 'null']
`
	if err := os.WriteFile(path, []byte(spec), 0o600); err != nil {
		t.Fatal(err)
	}
	validator, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	record := message.New("events", nil, []byte("null"), nil)
	if findings := validator.Validate([]message.Record{record}); len(findings) != 0 {
		t.Fatalf("nullable union findings=%+v", findings)
	}
}
