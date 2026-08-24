// Package agentotel normalizes OpenTelemetry JSON exports into a redacted,
// framework-neutral correlation log for agent reliability runs. Raw prompts,
// tool arguments, and tool results are never copied to the normalized output.
package agentotel

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"infernosim/pkg/reporting"
)

const (
	Version          = 1
	MaximumInputSize = 64 * 1024 * 1024
)

type Span struct {
	Version       int               `json:"version"`
	TraceID       string            `json:"trace_id"`
	SpanID        string            `json:"span_id"`
	ParentSpanID  string            `json:"parent_span_id,omitempty"`
	Operation     string            `json:"operation,omitempty"`
	Provider      string            `json:"provider,omitempty"`
	ToolName      string            `json:"tool_name,omitempty"`
	ToolCallID    string            `json:"tool_call_id,omitempty"`
	ContentHashes map[string]string `json:"content_hashes,omitempty"`
}

type ImportOptions struct {
	InputPath   string
	OutputPath  string
	HashContent bool
}

func Import(opts ImportOptions) ([]Span, error) {
	info, err := os.Stat(opts.InputPath)
	if err != nil {
		return nil, err
	}
	if info.Size() > MaximumInputSize {
		return nil, fmt.Errorf("OpenTelemetry input exceeds %d bytes", MaximumInputSize)
	}
	data, err := os.ReadFile(filepath.Clean(opts.InputPath))
	if err != nil {
		return nil, err
	}
	spans, err := parse(data, opts.HashContent)
	if err != nil {
		return nil, err
	}
	if len(spans) == 0 {
		return nil, fmt.Errorf("OpenTelemetry input contains no agent-correlatable spans")
	}
	var output strings.Builder
	encoder := json.NewEncoder(&output)
	for _, span := range spans {
		if err := encoder.Encode(span); err != nil {
			return nil, err
		}
	}
	if err := reporting.WritePrivateFile(opts.OutputPath, []byte(output.String())); err != nil {
		return nil, err
	}
	return spans, nil
}

func parse(data []byte, hashContent bool) ([]Span, error) {
	var document any
	if json.Unmarshal(data, &document) == nil {
		if object, ok := document.(map[string]any); ok {
			resourceValue := firstValue(object, "resourceSpans", "resource_spans")
			if resourceSpans, exists := resourceValue.([]any); exists {
				return parseOTLP(resourceSpans, hashContent), nil
			}
			if span, ok := normalizeSpan(object, hashContent); ok {
				return []Span{span}, nil
			}
		}
		if values, ok := document.([]any); ok {
			var spans []Span
			for _, value := range values {
				if object, ok := value.(map[string]any); ok {
					if span, accepted := normalizeSpan(object, hashContent); accepted {
						spans = append(spans, span)
					}
				}
			}
			return spans, nil
		}
	}
	var spans []Span
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		var object map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &object); err != nil {
			return nil, fmt.Errorf("parse OpenTelemetry JSONL: %w", err)
		}
		if span, accepted := normalizeSpan(object, hashContent); accepted {
			spans = append(spans, span)
		}
	}
	return spans, scanner.Err()
}

func parseOTLP(resourceSpans []any, hashContent bool) []Span {
	var spans []Span
	for _, resourceValue := range resourceSpans {
		resource, _ := resourceValue.(map[string]any)
		scopeSpans, _ := firstValue(resource, "scopeSpans", "scope_spans").([]any)
		for _, scopeValue := range scopeSpans {
			scope, _ := scopeValue.(map[string]any)
			spanValues, _ := scope["spans"].([]any)
			for _, spanValue := range spanValues {
				object, _ := spanValue.(map[string]any)
				if span, accepted := normalizeSpan(object, hashContent); accepted {
					spans = append(spans, span)
				}
			}
		}
	}
	return spans
}

func normalizeSpan(object map[string]any, hashContent bool) (Span, bool) {
	attributes := attributeMap(object["attributes"])
	operation := attributeString(attributes, "gen_ai.operation.name")
	toolName := firstNonEmpty(attributeString(attributes, "gen_ai.tool.name"), attributeString(attributes, "gen_ai.tool.call.name"))
	toolCallID := attributeString(attributes, "gen_ai.tool.call.id")
	if operation == "" && toolName == "" && toolCallID == "" {
		return Span{}, false
	}
	span := Span{
		Version:      Version,
		TraceID:      metadata(firstValue(object, "traceId", "trace_id"), 256),
		SpanID:       metadata(firstValue(object, "spanId", "span_id"), 256),
		ParentSpanID: metadata(firstValue(object, "parentSpanId", "parent_span_id"), 256),
		Operation:    metadata(operation, 512), Provider: metadata(attributeString(attributes, "gen_ai.provider.name"), 512),
		ToolName: metadata(toolName, 512), ToolCallID: metadata(toolCallID, 512),
	}
	if span.Operation == "" && span.ToolName == "" && span.ToolCallID == "" {
		return Span{}, false
	}
	if hashContent {
		for _, key := range []string{"gen_ai.tool.call.arguments", "gen_ai.tool.call.result", "gen_ai.input.messages", "gen_ai.output.messages"} {
			if value := attributeString(attributes, key); value != "" {
				if span.ContentHashes == nil {
					span.ContentHashes = make(map[string]string)
				}
				hash := sha256.Sum256([]byte(value))
				span.ContentHashes[key] = hex.EncodeToString(hash[:])
			}
		}
	}
	return span, true
}

func attributeMap(value any) map[string]any {
	if object, ok := value.(map[string]any); ok {
		return object
	}
	result := make(map[string]any)
	entries, _ := value.([]any)
	for _, entryValue := range entries {
		entry, _ := entryValue.(map[string]any)
		key, _ := entry["key"].(string)
		valueObject, _ := entry["value"].(map[string]any)
		for _, field := range []string{"stringValue", "intValue", "boolValue", "doubleValue"} {
			if item, exists := valueObject[field]; exists {
				result[key] = item
				break
			}
		}
	}
	return result
}

func attributeString(attributes map[string]any, key string) string {
	value := attributes[key]
	if object, ok := value.(map[string]any); ok {
		for _, field := range []string{"stringValue", "intValue", "boolValue", "doubleValue"} {
			if item, exists := object[field]; exists {
				return scalar(item)
			}
		}
	}
	return scalar(value)
}

func scalar(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case json.Number:
		return typed.String()
	case nil:
		return ""
	default:
		encoded, _ := json.Marshal(typed)
		return strings.Trim(string(encoded), `"`)
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func firstValue(object map[string]any, keys ...string) any {
	for _, key := range keys {
		if value, exists := object[key]; exists {
			return value
		}
	}
	return nil
}

func metadata(value any, maximum int) string {
	text := strings.TrimSpace(scalar(value))
	if len(text) > maximum || strings.ContainsAny(text, "\r\n\x00") {
		return ""
	}
	return text
}
