package agentotel

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestGenericParentAndNormalizedEvidenceRoundTrip(t *testing.T) {
	doc := `{"resourceSpans":[{"scopeSpans":[{"spans":[{"traceId":"trace","spanId":"parent","name":"private workflow"},{"traceId":"trace","spanId":"child","parentSpanId":"parent","startTimeUnixNano":"1234567890123456789","endTimeUnixNano":"1234567890123456799","links":[{"traceId":"other","spanId":"linked","attributes":[{"key":"secret","value":{"stringValue":"do not copy"}}]}],"attributes":[{"key":"gen_ai.tool.call.id","value":{"stringValue":"call"}},{"key":"gen_ai.tool.call.arguments","value":{"stringValue":"sensitive payload"}}]}]}]}]}`
	spans, err := parse([]byte(doc), true)
	if err != nil || len(spans) != 2 {
		t.Fatalf("lost generic parent: %+v %v", spans, err)
	}
	if spans[1].ParentSpanID != "parent" || spans[1].StartTimeUnixNano != "1234567890123456789" || len(spans[1].Links) != 1 || len(spans[1].ContentHashes) != 1 {
		t.Fatalf("lost evidence: %+v", spans[1])
	}
	var lines []byte
	for _, s := range spans {
		b, _ := json.Marshal(s)
		lines = append(lines, append(b, '\n')...)
	}
	again, err := Parse(lines)
	if err != nil || !reflect.DeepEqual(spans, again) {
		t.Fatalf("round trip differs: %+v %v", again, err)
	}
	for _, secret := range []string{"private workflow", "do not copy", "sensitive payload"} {
		if strings.Contains(string(lines), secret) {
			t.Fatal("raw telemetry leaked")
		}
	}
}

func TestImportOTLPRedactsContent(t *testing.T) {
	directory := t.TempDir()
	input := filepath.Join(directory, "otel.json")
	output := filepath.Join(directory, "agent-spans.jsonl")
	document := `{"resourceSpans":[{"scopeSpans":[{"spans":[{"traceId":"trace-1","spanId":"span-1","name":"RAW_PROMPT_IN_SPAN_NAME","attributes":[{"key":"gen_ai.operation.name","value":{"stringValue":"execute_tool"}},{"key":"gen_ai.tool.name","value":{"stringValue":"payment.refund"}},{"key":"gen_ai.tool.call.id","value":{"stringValue":"call-1"}},{"key":"gen_ai.tool.call.arguments","value":{"stringValue":"SECRET_PAYMENT_ARGUMENT"}}]}]}]}]}`
	if err := os.WriteFile(input, []byte(document), 0o600); err != nil {
		t.Fatal(err)
	}
	spans, err := Import(ImportOptions{InputPath: input, OutputPath: output, HashContent: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(spans) != 1 || spans[0].ToolName != "payment.refund" || spans[0].ContentHashes["gen_ai.tool.call.arguments"] == "" {
		t.Fatalf("spans = %#v", spans)
	}
	stored, _ := os.ReadFile(output)
	if strings.Contains(string(stored), "SECRET_PAYMENT_ARGUMENT") {
		t.Fatalf("raw content leaked: %s", stored)
	}
	if strings.Contains(string(stored), "RAW_PROMPT_IN_SPAN_NAME") {
		t.Fatalf("untrusted span name leaked: %s", stored)
	}
}

func TestImportNormalizedSnakeCaseSpan(t *testing.T) {
	directory := t.TempDir()
	input := filepath.Join(directory, "otel.jsonl")
	output := filepath.Join(directory, "agent-spans.jsonl")
	document := `{"trace_id":"trace-2","span_id":"span-2","attributes":{"gen_ai.operation.name":"execute_tool","gen_ai.tool.name":"inventory.lookup"}}` + "\n"
	if err := os.WriteFile(input, []byte(document), 0o600); err != nil {
		t.Fatal(err)
	}
	spans, err := Import(ImportOptions{InputPath: input, OutputPath: output})
	if err != nil {
		t.Fatal(err)
	}
	if len(spans) != 1 || spans[0].TraceID != "trace-2" || spans[0].ToolName != "inventory.lookup" {
		t.Fatalf("spans = %#v", spans)
	}
}
