package agentotel

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
