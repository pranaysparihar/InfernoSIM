package contract

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"infernosim/pkg/event"
	"infernosim/pkg/reporting"

	"gopkg.in/yaml.v3"
)

type Document struct {
	OpenAPI    string              `yaml:"openapi"`
	Paths      map[string]PathItem `yaml:"paths"`
	Components Components          `yaml:"components"`
}

type Components struct {
	Schemas map[string]*Schema `yaml:"schemas"`
}

type PathItem struct {
	Get     *Operation `yaml:"get"`
	Post    *Operation `yaml:"post"`
	Put     *Operation `yaml:"put"`
	Patch   *Operation `yaml:"patch"`
	Delete  *Operation `yaml:"delete"`
	Head    *Operation `yaml:"head"`
	Options *Operation `yaml:"options"`
	Trace   *Operation `yaml:"trace"`
}

type Operation struct {
	OperationID string              `yaml:"operationId"`
	RequestBody *RequestBody        `yaml:"requestBody"`
	Responses   map[string]Response `yaml:"responses"`
}

type RequestBody struct {
	Required bool                 `yaml:"required"`
	Content  map[string]MediaType `yaml:"content"`
}

type Response struct {
	Description string               `yaml:"description"`
	Headers     map[string]Header    `yaml:"headers"`
	Content     map[string]MediaType `yaml:"content"`
}

type Header struct {
	Required bool    `yaml:"required"`
	Schema   *Schema `yaml:"schema"`
}

type MediaType struct {
	Schema *Schema `yaml:"schema"`
}

type Schema struct {
	Ref        string             `yaml:"$ref"`
	Type       any                `yaml:"type"`
	Required   []string           `yaml:"required"`
	Properties map[string]*Schema `yaml:"properties"`
	Items      *Schema            `yaml:"items"`
	Enum       []any              `yaml:"enum"`
	Nullable   bool               `yaml:"nullable"`
}

type Validator struct {
	document Document
	specPath string
}

func Load(path string) (*Validator, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("load OpenAPI document %q: %w", path, err)
	}
	var document Document
	if err := yaml.Unmarshal(data, &document); err != nil {
		return nil, fmt.Errorf("parse OpenAPI document %q: %w", path, err)
	}
	if !strings.HasPrefix(document.OpenAPI, "3.") {
		return nil, fmt.Errorf("OpenAPI document must use version 3.x")
	}
	if len(document.Paths) == 0 {
		return nil, fmt.Errorf("OpenAPI document contains no paths")
	}
	return &Validator{document: document, specPath: path}, nil
}

func (v *Validator) Document() Document {
	if v == nil {
		return Document{}
	}
	return v.document
}

func (v *Validator) ValidateEvents(events []event.Event, phase string) []reporting.Finding {
	var findings []reporting.Finding
	for index, captured := range events {
		location := fmt.Sprintf("%s#event-%d", phase, index+1)
		parsed, err := url.Parse(captured.URL)
		if err != nil {
			findings = append(findings, finding("OPENAPI_INVALID_URL", "Captured URL is invalid", err.Error(), location))
			continue
		}
		pathTemplate, operation := v.operation(strings.ToUpper(captured.Method), parsed.Path)
		if operation == nil {
			findings = append(findings, finding(
				"OPENAPI_UNDOCUMENTED_OPERATION",
				"Operation is not documented",
				fmt.Sprintf("%s %s is not present in the OpenAPI contract", captured.Method, parsed.Path),
				location,
			))
			continue
		}
		if operation.RequestBody != nil {
			findings = append(findings, v.validateRequest(captured, operation, location)...)
		}
		if captured.Status > 0 {
			findings = append(findings, v.validateResponse(captured, operation, pathTemplate, location)...)
		}
	}
	return findings
}

func (v *Validator) validateRequest(captured event.Event, operation *Operation, location string) []reporting.Finding {
	if captured.BodyB64 == "" {
		if operation.RequestBody.Required && captured.BodySize == 0 {
			return []reporting.Finding{finding(
				"OPENAPI_REQUIRED_REQUEST_BODY",
				"Required request body is missing",
				"The OpenAPI operation requires a request body",
				location,
			)}
		}
		return nil
	}
	body, err := base64.StdEncoding.DecodeString(captured.BodyB64)
	if err != nil {
		return []reporting.Finding{finding("OPENAPI_INVALID_BODY_ENCODING", "Request body cannot be decoded", err.Error(), location)}
	}
	contentType := headerValue(captured.Headers, "Content-Type")
	schema := selectSchema(operation.RequestBody.Content, contentType)
	return v.validatePayload(schema, body, "request", location)
}

func (v *Validator) validateResponse(captured event.Event, operation *Operation, pathTemplate, location string) []reporting.Finding {
	response, ok := responseForStatus(operation.Responses, captured.Status)
	if !ok {
		return []reporting.Finding{finding(
			"OPENAPI_UNDOCUMENTED_STATUS",
			"Response status is not documented",
			fmt.Sprintf("%s returned status %d, which is absent from %s", captured.Method, captured.Status, pathTemplate),
			location,
		)}
	}
	var findings []reporting.Finding
	for name, header := range response.Headers {
		if header.Required && headerValue(captured.ResponseHeaders, name) == "" {
			findings = append(findings, finding(
				"OPENAPI_REQUIRED_RESPONSE_HEADER",
				"Required response header is missing",
				fmt.Sprintf("response status %d is missing required header %s", captured.Status, name),
				location,
			))
		}
	}
	if captured.ResponseBodyB64 == "" {
		return findings
	}
	body, err := base64.StdEncoding.DecodeString(captured.ResponseBodyB64)
	if err != nil {
		return append(findings, finding("OPENAPI_INVALID_BODY_ENCODING", "Response body cannot be decoded", err.Error(), location))
	}
	contentType := headerValue(captured.ResponseHeaders, "Content-Type")
	schema := selectSchema(response.Content, contentType)
	return append(findings, v.validatePayload(schema, body, "response", location)...)
}

func (v *Validator) validatePayload(schema *Schema, body []byte, direction, location string) []reporting.Finding {
	if schema == nil || len(body) == 0 {
		return nil
	}
	var value any
	if err := json.Unmarshal(body, &value); err != nil {
		return []reporting.Finding{finding(
			"OPENAPI_INVALID_JSON",
			"Payload is not valid JSON",
			fmt.Sprintf("%s body: %v", direction, err),
			location,
		)}
	}
	var violations []string
	v.validateSchema(schema, value, "$", &violations, map[string]bool{})
	findings := make([]reporting.Finding, 0, len(violations))
	for _, violation := range violations {
		findings = append(findings, finding(
			"OPENAPI_SCHEMA_VIOLATION",
			"Payload violates its OpenAPI schema",
			fmt.Sprintf("%s body %s", direction, violation),
			location,
		))
	}
	return findings
}

func (v *Validator) validateSchema(schema *Schema, value any, path string, violations *[]string, resolving map[string]bool) {
	if schema == nil {
		return
	}
	if schema.Ref != "" {
		const prefix = "#/components/schemas/"
		if !strings.HasPrefix(schema.Ref, prefix) {
			*violations = append(*violations, fmt.Sprintf("%s uses unsupported external schema reference %s", path, schema.Ref))
			return
		}
		name := strings.TrimPrefix(schema.Ref, prefix)
		if resolving[name] {
			return
		}
		target := v.document.Components.Schemas[name]
		if target == nil {
			*violations = append(*violations, fmt.Sprintf("%s references missing schema %s", path, name))
			return
		}
		resolving[name] = true
		v.validateSchema(target, value, path, violations, resolving)
		delete(resolving, name)
		return
	}
	if value == nil && schema.Nullable {
		return
	}
	expectedType := schemaType(schema.Type)
	if expectedType != "" && !matchesType(expectedType, value) {
		*violations = append(*violations, fmt.Sprintf("%s expected %s but received %T", path, expectedType, value))
		return
	}
	if len(schema.Enum) > 0 && !enumContains(schema.Enum, value) {
		*violations = append(*violations, fmt.Sprintf("%s is not one of the permitted enum values", path))
	}
	switch object := value.(type) {
	case map[string]any:
		for _, required := range schema.Required {
			if _, ok := object[required]; !ok {
				*violations = append(*violations, fmt.Sprintf("%s.%s is required", path, required))
			}
		}
		for name, property := range schema.Properties {
			if child, ok := object[name]; ok {
				v.validateSchema(property, child, path+"."+name, violations, resolving)
			}
		}
	case []any:
		for index, child := range object {
			v.validateSchema(schema.Items, child, fmt.Sprintf("%s[%d]", path, index), violations, resolving)
		}
	}
}

func (v *Validator) operation(method, actualPath string) (string, *Operation) {
	if item, ok := v.document.Paths[actualPath]; ok {
		return actualPath, operationForMethod(item, method)
	}
	var templates []string
	for pathTemplate := range v.document.Paths {
		if pathTemplate != actualPath {
			templates = append(templates, pathTemplate)
		}
	}
	sort.Slice(templates, func(i, j int) bool {
		return literalPathLength(templates[i]) > literalPathLength(templates[j])
	})
	for _, pathTemplate := range templates {
		if !pathMatches(pathTemplate, actualPath) {
			continue
		}
		return pathTemplate, operationForMethod(v.document.Paths[pathTemplate], method)
	}
	return "", nil
}

func operationForMethod(item PathItem, method string) *Operation {
	switch method {
	case http.MethodGet:
		return item.Get
	case http.MethodPost:
		return item.Post
	case http.MethodPut:
		return item.Put
	case http.MethodPatch:
		return item.Patch
	case http.MethodDelete:
		return item.Delete
	case http.MethodHead:
		return item.Head
	case http.MethodOptions:
		return item.Options
	case http.MethodTrace:
		return item.Trace
	default:
		return nil
	}
}

func literalPathLength(path string) int {
	length := 0
	inParameter := false
	for _, character := range path {
		switch character {
		case '{':
			inParameter = true
		case '}':
			inParameter = false
		default:
			if !inParameter {
				length++
			}
		}
	}
	return length
}

func responseForStatus(responses map[string]Response, status int) (Response, bool) {
	if response, ok := responses[strconv.Itoa(status)]; ok {
		return response, true
	}
	class := fmt.Sprintf("%dXX", status/100)
	for key, response := range responses {
		if strings.EqualFold(key, class) {
			return response, true
		}
	}
	response, ok := responses["default"]
	return response, ok
}

func pathMatches(template, actual string) bool {
	if template == actual {
		return true
	}
	var expression strings.Builder
	expression.WriteByte('^')
	for index := 0; index < len(template); {
		if template[index] == '{' {
			end := strings.IndexByte(template[index:], '}')
			if end < 0 {
				return false
			}
			expression.WriteString(`[^/]+`)
			index += end + 1
			continue
		}
		expression.WriteString(regexp.QuoteMeta(string(template[index])))
		index++
	}
	expression.WriteByte('$')
	matched, _ := regexp.MatchString(expression.String(), actual)
	return matched
}

func selectSchema(content map[string]MediaType, contentType string) *Schema {
	if len(content) == 0 {
		return nil
	}
	contentType = strings.TrimSpace(strings.Split(contentType, ";")[0])
	if media, ok := content[contentType]; ok {
		return media.Schema
	}
	if media, ok := content["application/json"]; ok {
		return media.Schema
	}
	for _, media := range content {
		return media.Schema
	}
	return nil
}

func schemaType(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case []any:
		for _, item := range typed {
			if text, ok := item.(string); ok && text != "null" {
				return text
			}
		}
	}
	return ""
}

func matchesType(expected string, value any) bool {
	switch expected {
	case "object":
		_, ok := value.(map[string]any)
		return ok
	case "array":
		_, ok := value.([]any)
		return ok
	case "string":
		_, ok := value.(string)
		return ok
	case "number":
		_, ok := value.(float64)
		return ok
	case "integer":
		number, ok := value.(float64)
		return ok && number == float64(int64(number))
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "null":
		return value == nil
	default:
		return true
	}
}

func enumContains(values []any, actual any) bool {
	actualJSON, _ := json.Marshal(actual)
	for _, candidate := range values {
		candidateJSON, _ := json.Marshal(candidate)
		if string(actualJSON) == string(candidateJSON) {
			return true
		}
	}
	return false
}

func headerValue(headers map[string][]string, name string) string {
	for candidate, values := range headers {
		if strings.EqualFold(candidate, name) && len(values) > 0 {
			return values[0]
		}
	}
	return ""
}

func finding(ruleID, title, message, location string) reporting.Finding {
	return reporting.Finding{
		RuleID:   ruleID,
		Level:    "error",
		Title:    title,
		Message:  message,
		Location: location,
	}
}

// DriftFindings converts response differences into release-gate findings.
func DriftFindings(baseline, candidate []event.Event) []reporting.Finding {
	var findings []reporting.Finding
	limit := len(baseline)
	if len(candidate) < limit {
		limit = len(candidate)
	}
	for index := 0; index < limit; index++ {
		expected := baseline[index]
		actual := candidate[index]
		location := fmt.Sprintf("candidate#event-%d", index+1)
		if expected.Status != actual.Status {
			findings = append(findings, finding(
				"CONTRACT_STATUS_DRIFT",
				"Response status changed",
				fmt.Sprintf("%s %s changed from %d to %d", expected.Method, expected.URL, expected.Status, actual.Status),
				location,
			))
		}
		expectedType := headerValue(expected.ResponseHeaders, "Content-Type")
		actualType := headerValue(actual.ResponseHeaders, "Content-Type")
		if expectedType != "" && normalizeContentType(expectedType) != normalizeContentType(actualType) {
			findings = append(findings, finding(
				"CONTRACT_CONTENT_TYPE_DRIFT",
				"Response content type changed",
				fmt.Sprintf("%s changed from %q to %q", expected.URL, expectedType, actualType),
				location,
			))
		}
	}
	if len(candidate) < len(baseline) {
		findings = append(findings, finding(
			"CONTRACT_MISSING_RESPONSES",
			"Candidate returned fewer responses",
			fmt.Sprintf("candidate produced %d responses for %d baseline events", len(candidate), len(baseline)),
			"candidate",
		))
	}
	return findings
}

func normalizeContentType(value string) string {
	return strings.ToLower(strings.TrimSpace(strings.Split(value, ";")[0]))
}
