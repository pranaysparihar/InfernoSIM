package privacy

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

type Action string

const (
	ActionRedact   Action = "redact"
	ActionTokenize Action = "tokenize"
	ActionDrop     Action = "drop"
)

type NamedRule struct {
	Name   string `yaml:"name" json:"name"`
	Action Action `yaml:"action" json:"action"`
}

type JSONRule struct {
	Path   string `yaml:"path" json:"path"`
	Action Action `yaml:"action" json:"action"`
}

type MessageHeader struct {
	Name  string
	Value []byte
}

// Policy is the strict schema for privacy.yaml.
type Policy struct {
	Version         int         `yaml:"version" json:"version"`
	CaptureBodies   bool        `yaml:"capture_bodies" json:"capture_bodies"`
	TokenKeyEnv     string      `yaml:"token_key_env" json:"token_key_env,omitempty"`
	Headers         []NamedRule `yaml:"headers" json:"headers,omitempty"`
	QueryParameters []NamedRule `yaml:"query_parameters" json:"query_parameters,omitempty"`
	JSONFields      []JSONRule  `yaml:"json_fields" json:"json_fields,omitempty"`
	MessageKey      Action      `yaml:"message_key" json:"message_key,omitempty"`
	MessageHeaders  []NamedRule `yaml:"message_headers" json:"message_headers,omitempty"`

	tokenKey []byte
}

func Load(path string) (*Policy, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("load privacy policy %q: %w", path, err)
	}
	var policy Policy
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&policy); err != nil {
		return nil, fmt.Errorf("parse privacy policy %q: %w", path, err)
	}
	if policy.Version != 1 {
		return nil, fmt.Errorf("privacy policy version must be 1")
	}
	needsKey := false
	headerNames := make(map[string]bool)
	for _, rule := range policy.Headers {
		if err := validateNamedRule("headers", rule); err != nil {
			return nil, err
		}
		canonical := strings.ToLower(rule.Name)
		if headerNames[canonical] {
			return nil, fmt.Errorf("headers rule %q is duplicated", rule.Name)
		}
		headerNames[canonical] = true
		needsKey = needsKey || rule.Action == ActionTokenize
	}
	queryNames := make(map[string]bool)
	for _, rule := range policy.QueryParameters {
		if err := validateNamedRule("query_parameters", rule); err != nil {
			return nil, err
		}
		if queryNames[rule.Name] {
			return nil, fmt.Errorf("query_parameters rule %q is duplicated", rule.Name)
		}
		queryNames[rule.Name] = true
		needsKey = needsKey || rule.Action == ActionTokenize
	}
	jsonPaths := make(map[string]bool)
	for _, rule := range policy.JSONFields {
		if strings.TrimSpace(rule.Path) == "" {
			return nil, fmt.Errorf("json_fields path is required")
		}
		if _, err := parsePath(rule.Path); err != nil {
			return nil, fmt.Errorf("json_fields path %q: %w", rule.Path, err)
		}
		if err := validateAction(rule.Action); err != nil {
			return nil, fmt.Errorf("json_fields path %q: %w", rule.Path, err)
		}
		if jsonPaths[rule.Path] {
			return nil, fmt.Errorf("json_fields path %q is duplicated", rule.Path)
		}
		jsonPaths[rule.Path] = true
		needsKey = needsKey || rule.Action == ActionTokenize
	}
	if policy.MessageKey != "" {
		if err := validateAction(policy.MessageKey); err != nil {
			return nil, fmt.Errorf("message_key: %w", err)
		}
		needsKey = needsKey || policy.MessageKey == ActionTokenize
	}
	messageHeaderNames := make(map[string]bool)
	for _, rule := range policy.MessageHeaders {
		if err := validateNamedRule("message_headers", rule); err != nil {
			return nil, err
		}
		canonical := strings.ToLower(rule.Name)
		if messageHeaderNames[canonical] {
			return nil, fmt.Errorf("message_headers rule %q is duplicated", rule.Name)
		}
		messageHeaderNames[canonical] = true
		needsKey = needsKey || rule.Action == ActionTokenize
	}
	if needsKey {
		if policy.TokenKeyEnv == "" {
			return nil, fmt.Errorf("token_key_env is required when a tokenize rule is configured")
		}
		policy.tokenKey = []byte(os.Getenv(policy.TokenKeyEnv))
		if len(policy.tokenKey) < 16 {
			return nil, fmt.Errorf("environment variable %s must contain at least 16 bytes", policy.TokenKeyEnv)
		}
	}
	return &policy, nil
}

// ApplyMessage sanitizes a Kafka-compatible message key, headers, and JSON
// payload using the same deterministic local policy as HTTP capture.
func (p *Policy) ApplyMessage(key []byte, headers []MessageHeader, payload []byte) ([]byte, []MessageHeader, []byte, error) {
	cleanKey := append([]byte(nil), key...)
	cleanHeaders := make([]MessageHeader, 0, len(headers))
	for _, header := range headers {
		cleanHeaders = append(cleanHeaders, MessageHeader{Name: header.Name, Value: append([]byte(nil), header.Value...)})
	}
	cleanPayload := append([]byte(nil), payload...)
	if p == nil {
		return cleanKey, cleanHeaders, cleanPayload, nil
	}
	if p.MessageKey != "" {
		cleanKey = p.applyBytes(cleanKey, p.MessageKey)
	}
	for _, rule := range p.MessageHeaders {
		filtered := cleanHeaders[:0]
		for _, header := range cleanHeaders {
			if !strings.EqualFold(header.Name, rule.Name) {
				filtered = append(filtered, header)
				continue
			}
			if rule.Action == ActionDrop {
				continue
			} else {
				header.Value = p.applyBytes(header.Value, rule.Action)
				filtered = append(filtered, header)
			}
		}
		cleanHeaders = filtered
	}
	if len(cleanPayload) > 0 && len(p.JSONFields) > 0 {
		var err error
		cleanPayload, err = p.ApplyBody(cleanPayload)
		if err != nil {
			return nil, nil, nil, err
		}
	}
	return cleanKey, cleanHeaders, cleanPayload, nil
}

func (p *Policy) applyBytes(value []byte, action Action) []byte {
	switch action {
	case ActionDrop:
		return nil
	case ActionRedact:
		return []byte("[REDACTED]")
	case ActionTokenize:
		return []byte(p.token(string(value)))
	default:
		return append([]byte(nil), value...)
	}
}

func validateNamedRule(section string, rule NamedRule) error {
	if strings.TrimSpace(rule.Name) == "" {
		return fmt.Errorf("%s rule name is required", section)
	}
	if rule.Name != strings.TrimSpace(rule.Name) {
		return fmt.Errorf("%s rule name %q cannot have leading or trailing whitespace", section, rule.Name)
	}
	if err := validateAction(rule.Action); err != nil {
		return fmt.Errorf("%s rule %q: %w", section, rule.Name, err)
	}
	return nil
}

func validateAction(action Action) error {
	switch action {
	case ActionRedact, ActionTokenize, ActionDrop:
		return nil
	default:
		return fmt.Errorf("action must be redact, tokenize, or drop")
	}
}

func (p *Policy) HeaderRule(name string) (Action, bool) {
	if p == nil {
		return "", false
	}
	for _, rule := range p.Headers {
		if strings.EqualFold(rule.Name, name) {
			return rule.Action, true
		}
	}
	return "", false
}

func (p *Policy) ApplyHeaders(headers http.Header) http.Header {
	out := headers.Clone()
	if p == nil {
		return out
	}
	for _, rule := range p.Headers {
		for actualName, originalValues := range out {
			if !strings.EqualFold(actualName, rule.Name) {
				continue
			}
			values := append([]string(nil), originalValues...)
			switch rule.Action {
			case ActionDrop:
				delete(out, actualName)
			case ActionRedact:
				out[actualName] = []string{"[REDACTED]"}
			case ActionTokenize:
				for i := range values {
					values[i] = p.token(values[i])
				}
				out[actualName] = values
			}
		}
	}
	return out
}

func (p *Policy) ApplyURL(input *url.URL) *url.URL {
	if input == nil {
		return nil
	}
	out := *input
	if p == nil {
		return &out
	}
	query := out.Query()
	for _, rule := range p.QueryParameters {
		values, ok := query[rule.Name]
		if !ok {
			continue
		}
		switch rule.Action {
		case ActionDrop:
			query.Del(rule.Name)
		case ActionRedact:
			query[rule.Name] = []string{"[REDACTED]"}
		case ActionTokenize:
			for i := range values {
				values[i] = p.token(values[i])
			}
			query[rule.Name] = values
		}
	}
	out.RawQuery = query.Encode()
	return &out
}

func (p *Policy) ApplyBody(body []byte) ([]byte, error) {
	if p == nil || len(p.JSONFields) == 0 || len(body) == 0 {
		return append([]byte(nil), body...), nil
	}
	var value any
	if err := json.Unmarshal(body, &value); err != nil {
		return nil, fmt.Errorf("privacy policy JSON rules require a JSON body: %w", err)
	}
	for _, rule := range p.JSONFields {
		applyJSONRule(value, rule, p.token)
	}
	return json.Marshal(value)
}

func (p *Policy) token(value string) string {
	mac := hmac.New(sha256.New, p.tokenKey)
	_, _ = mac.Write([]byte(value))
	return "tok_" + hex.EncodeToString(mac.Sum(nil)[:16])
}

type pathPart struct {
	key     string
	index   int
	isIndex bool
}

func parsePath(path string) ([]pathPart, error) {
	if path == "$" {
		return nil, nil
	}
	if !strings.HasPrefix(path, "$.") {
		return nil, fmt.Errorf("must begin with $.")
	}
	input := path[2:]
	var out []pathPart
	for input != "" {
		keyEnd := strings.IndexAny(input, ".[")
		if keyEnd < 0 {
			keyEnd = len(input)
		}
		if keyEnd > 0 {
			out = append(out, pathPart{key: input[:keyEnd]})
			input = input[keyEnd:]
		}
		if strings.HasPrefix(input, ".") {
			input = input[1:]
			continue
		}
		if strings.HasPrefix(input, "[") {
			end := strings.IndexByte(input, ']')
			if end < 2 {
				return nil, fmt.Errorf("invalid array index")
			}
			index, err := strconv.Atoi(input[1:end])
			if err != nil || index < 0 {
				return nil, fmt.Errorf("invalid array index")
			}
			out = append(out, pathPart{index: index, isIndex: true})
			input = input[end+1:]
			if strings.HasPrefix(input, ".") {
				input = input[1:]
			}
		}
	}
	return out, nil
}

func applyJSONRule(root any, rule JSONRule, tokenizer func(string) string) {
	parts, err := parsePath(rule.Path)
	if err != nil || len(parts) == 0 {
		return
	}
	current := root
	for _, part := range parts[:len(parts)-1] {
		if part.isIndex {
			array, ok := current.([]any)
			if !ok || part.index >= len(array) {
				return
			}
			current = array[part.index]
		} else {
			object, ok := current.(map[string]any)
			if !ok {
				return
			}
			current, ok = object[part.key]
			if !ok {
				return
			}
		}
	}
	last := parts[len(parts)-1]
	if last.isIndex {
		array, ok := current.([]any)
		if !ok || last.index >= len(array) {
			return
		}
		switch rule.Action {
		case ActionDrop, ActionRedact:
			array[last.index] = "[REDACTED]"
		case ActionTokenize:
			array[last.index] = tokenizer(valueString(array[last.index]))
		}
		return
	}
	object, ok := current.(map[string]any)
	if !ok {
		return
	}
	value, exists := object[last.key]
	if !exists {
		return
	}
	switch rule.Action {
	case ActionDrop:
		delete(object, last.key)
	case ActionRedact:
		object[last.key] = "[REDACTED]"
	case ActionTokenize:
		object[last.key] = tokenizer(valueString(value))
	}
}

func valueString(value any) string {
	if stringValue, ok := value.(string); ok {
		return stringValue
	}
	encoded, _ := json.Marshal(value)
	return string(encoded)
}
