package workflow

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"infernosim/pkg/event"
	"infernosim/pkg/message"
)

func TestVerifyCrossProtocolWorkflow(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	writeEvents(t, filepath.Join(dir, "inbound.log"), []event.Event{{
		ID: "one", Type: "InboundRequest", Timestamp: base, Sequence: 1, Method: "POST", URL: "http://service/payments", TraceID: "correlation-1",
	}})
	writeEvents(t, filepath.Join(dir, "outbound.log"), []event.Event{{
		ID: "two", Type: "OutboundCall", Timestamp: base.Add(time.Millisecond), Sequence: 1, Method: "POST", URL: "http://funds/reserve",
		GrpcServiceMethod: "/funds.Funds/Reserve", TraceID: "correlation-1",
	}})
	logger, err := message.NewLogger(filepath.Join(dir, "messages.log"))
	if err != nil {
		t.Fatal(err)
	}
	record := message.New("payment.authorized", nil, []byte(`{"ok":true}`), map[string][]byte{"x-correlation-id": []byte("correlation-1")})
	record.Timestamp = base.Add(2 * time.Millisecond)
	if err := logger.Write(record); err != nil {
		t.Fatal(err)
	}
	_ = logger.Close()
	config := []Config{{Name: "payment", Correlation: "required", Steps: []Step{
		{Name: "accept", Protocol: "http", Direction: "inbound", Method: "POST", PathRegex: `^/payments$`},
		{Name: "reserve", Protocol: "grpc", Direction: "outbound", GRPCMethod: "/funds.Funds/Reserve", Within: "10ms"},
		{Name: "event", Protocol: "kafka", Topic: "payment.authorized", Within: "10ms"},
	}}}
	findings, err := VerifyIncident(dir, config)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 0 {
		t.Fatalf("findings=%+v", findings)
	}
	config[0].Steps[2].Topic = "payment.failed"
	findings, err = VerifyIncident(dir, config)
	if err != nil || len(findings) != 1 || findings[0].RuleID != "WORKFLOW_MISSING_STEP" {
		t.Fatalf("findings=%+v err=%v", findings, err)
	}
}

func TestValidateConfigsRejectsInvalidWorkflow(t *testing.T) {
	if err := ValidateConfigs([]Config{{Name: "bad", Steps: []Step{{Name: "event", Protocol: "kafka"}}}}); err == nil {
		t.Fatal("expected topic validation error")
	}
}

func writeEvents(t *testing.T, path string, events []event.Event) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	encoder := json.NewEncoder(file)
	for _, captured := range events {
		if err := encoder.Encode(captured); err != nil {
			t.Fatal(err)
		}
	}
	_ = file.Close()
}
