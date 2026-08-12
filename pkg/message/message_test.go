package message

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRecordRoundTripAndIntegrity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "messages.log")
	logger, err := NewLogger(path)
	if err != nil {
		t.Fatal(err)
	}
	record := New("payment.authorized", []byte("payment-1"), []byte(`{"request_id":"request-1","ok":true}`), map[string][]byte{"type": []byte("authorized")})
	record.Timestamp = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := logger.Write(record); err != nil {
		t.Fatal(err)
	}
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}
	records, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].Sequence != 1 || records[0].CorrelationID != "request-1" {
		t.Fatalf("records=%+v", records)
	}
	payload, err := records[0].Payload()
	if err != nil || string(payload) != `{"request_id":"request-1","ok":true}` {
		t.Fatalf("payload=%s err=%v", payload, err)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode=%o", info.Mode().Perm())
	}
}

func TestRecordDetectsPayloadTampering(t *testing.T) {
	record := New("topic", nil, []byte("original"), nil)
	record.PayloadB64 = "dGFtcGVyZWQ="
	if err := record.Validate(); err == nil {
		t.Fatal("expected hash mismatch")
	}
}

func TestRecordPreservesDuplicateHeaderOrder(t *testing.T) {
	record := NewWithHeaders("topic", nil, []byte("value"), []BinaryHeader{
		{Key: "x-correlation-id", Value: []byte("first")},
		{Key: "X-Correlation-ID", Value: []byte("second")},
	})
	headers, err := record.HeaderValues()
	if err != nil {
		t.Fatal(err)
	}
	if len(headers) != 2 || string(headers[0].Value) != "first" || string(headers[1].Value) != "second" {
		t.Fatalf("headers=%+v", headers)
	}
	if record.CorrelationID != "first" {
		t.Fatalf("correlation selection is not ordered: %q", record.CorrelationID)
	}
}

func TestLoadRejectsDuplicateRecordIDs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "messages.log")
	logger, err := NewLogger(path)
	if err != nil {
		t.Fatal(err)
	}
	record := New("topic", nil, []byte("value"), nil)
	if err := logger.Write(record); err != nil {
		t.Fatal(err)
	}
	if err := logger.Write(record); err != nil {
		t.Fatal(err)
	}
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected duplicate message id rejection")
	}
}

func FuzzRecordValidate(f *testing.F) {
	f.Add("topic", "dmFsdWU=", SHA256([]byte("value")))
	f.Add("", "%%%", "bad")
	f.Fuzz(func(t *testing.T, topic, payloadB64, hash string) {
		record := Record{
			Version: FormatVersion, ID: "fuzz", Timestamp: time.Unix(1, 0).UTC(),
			Topic: topic, Partition: 0, Offset: 0, PayloadB64: payloadB64, PayloadSHA256: hash,
		}
		_ = record.Validate()
	})
}
