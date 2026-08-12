package asyncapi

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"regexp"
	"sort"
	"strings"

	"infernosim/pkg/message"
	"infernosim/pkg/reporting"

	"gopkg.in/yaml.v3"
)

type Document struct {
	AsyncAPI   string             `yaml:"asyncapi"`
	Channels   map[string]Channel `yaml:"channels"`
	Components Components         `yaml:"components"`
}

type Components struct {
	Messages map[string]*Message `yaml:"messages"`
	Schemas  map[string]*Schema  `yaml:"schemas"`
}

type Channel struct {
	Address  string              `yaml:"address"`
	Messages map[string]*Message `yaml:"messages"`
}

type Message struct {
	Ref         string  `yaml:"$ref"`
	Name        string  `yaml:"name"`
	Title       string  `yaml:"title"`
	ContentType string  `yaml:"contentType"`
	Headers     *Schema `yaml:"headers"`
	Payload     *Schema `yaml:"payload"`
}

type Schema struct {
	Ref                  string             `yaml:"$ref"`
	Type                 any                `yaml:"type"`
	Required             []string           `yaml:"required"`
	Properties           map[string]*Schema `yaml:"properties"`
	Items                *Schema            `yaml:"items"`
	Enum                 []any              `yaml:"enum"`
	Pattern              string             `yaml:"pattern"`
	AdditionalProperties any                `yaml:"additionalProperties"`
}

type Validator struct {
	document Document
	path     string
}

func Load(path string) (*Validator, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("load AsyncAPI document %q: %w", path, err)
	}
	var document Document
	if err := yaml.Unmarshal(data, &document); err != nil {
		return nil, fmt.Errorf("parse AsyncAPI document %q: %w", path, err)
	}
	if !strings.HasPrefix(document.AsyncAPI, "3.") {
		return nil, fmt.Errorf("AsyncAPI document must use version 3.x")
	}
	if len(document.Channels) == 0 {
		return nil, fmt.Errorf("AsyncAPI document contains no channels")
	}
	validator := &Validator{document: document, path: path}
	if err := validator.validateContract(); err != nil {
		return nil, fmt.Errorf("validate AsyncAPI document %q: %w", path, err)
	}
	return validator, nil
}

func (v *Validator) validateContract() error {
	for channelName, channel := range v.document.Channels {
		for messageName, definition := range channel.Messages {
			resolved, err := v.resolveMessage(definition)
			if err != nil {
				return fmt.Errorf("channels.%s.messages.%s: %w", channelName, messageName, err)
			}
			if err := v.validateSchemaDefinition(resolved.Headers, "channels."+channelName+".messages."+messageName+".headers", map[*Schema]bool{}); err != nil {
				return err
			}
			if !isJSONContentType(resolved.ContentType) {
				return fmt.Errorf("channels.%s.messages.%s contentType %q is outside the supported JSON subset", channelName, messageName, resolved.ContentType)
			}
			if err := v.validateSchemaDefinition(resolved.Payload, "channels."+channelName+".messages."+messageName+".payload", map[*Schema]bool{}); err != nil {
				return err
			}
		}
	}
	for messageName, definition := range v.document.Components.Messages {
		resolved, err := v.resolveMessage(definition)
		if err != nil {
			return fmt.Errorf("components.messages.%s: %w", messageName, err)
		}
		if !isJSONContentType(resolved.ContentType) {
			return fmt.Errorf("components.messages.%s contentType %q is outside the supported JSON subset", messageName, resolved.ContentType)
		}
		if err := v.validateSchemaDefinition(resolved.Headers, "components.messages."+messageName+".headers", map[*Schema]bool{}); err != nil {
			return err
		}
		if err := v.validateSchemaDefinition(resolved.Payload, "components.messages."+messageName+".payload", map[*Schema]bool{}); err != nil {
			return err
		}
	}
	for name, schema := range v.document.Components.Schemas {
		if err := v.validateSchemaDefinition(schema, "components.schemas."+name, map[*Schema]bool{}); err != nil {
			return err
		}
	}
	return nil
}

func isJSONContentType(value string) bool {
	if strings.TrimSpace(value) == "" {
		return true
	}
	mediaType := strings.ToLower(strings.TrimSpace(strings.Split(value, ";")[0]))
	return mediaType == "application/json" || strings.HasSuffix(mediaType, "+json")
}

func (v *Validator) validateSchemaDefinition(schema *Schema, path string, visiting map[*Schema]bool) error {
	if schema == nil || visiting[schema] {
		return nil
	}
	visiting[schema] = true
	defer delete(visiting, schema)
	if schema.Ref != "" {
		const prefix = "#/components/schemas/"
		if !strings.HasPrefix(schema.Ref, prefix) {
			return fmt.Errorf("%s uses unsupported external schema reference %s", path, schema.Ref)
		}
		name := strings.TrimPrefix(schema.Ref, prefix)
		if v.document.Components.Schemas[name] == nil {
			return fmt.Errorf("%s references missing schema %s", path, name)
		}
	}
	if _, err := schemaTypes(schema.Type); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if schema.Pattern != "" {
		if _, err := regexp.Compile(schema.Pattern); err != nil {
			return fmt.Errorf("%s contains invalid pattern: %w", path, err)
		}
	}
	required := make(map[string]bool)
	for _, name := range schema.Required {
		if required[name] {
			return fmt.Errorf("%s required property %q is duplicated", path, name)
		}
		required[name] = true
	}
	if schema.AdditionalProperties != nil {
		if _, ok := schema.AdditionalProperties.(bool); !ok {
			return fmt.Errorf("%s additionalProperties schemas are outside the supported JSON Schema subset", path)
		}
	}
	for name, child := range schema.Properties {
		if err := v.validateSchemaDefinition(child, path+".properties."+name, visiting); err != nil {
			return err
		}
	}
	return v.validateSchemaDefinition(schema.Items, path+".items", visiting)
}

func (v *Validator) Validate(records []message.Record) []reporting.Finding {
	var findings []reporting.Finding
	for index, record := range records {
		location := fmt.Sprintf("messages.log#message-%d", index+1)
		channelName, channel := v.channel(record.Topic)
		if channel == nil {
			findings = append(findings, finding(
				"ASYNCAPI_UNDOCUMENTED_CHANNEL", "Message channel is not documented",
				fmt.Sprintf("Kafka topic %s is absent from the AsyncAPI contract", record.Topic), location,
			))
			continue
		}
		messages, resolutionFindings := v.messages(*channel, location)
		findings = append(findings, resolutionFindings...)
		if len(messages) == 0 {
			continue
		}
		payload, err := record.Payload()
		if err != nil {
			findings = append(findings, finding("ASYNCAPI_INVALID_PAYLOAD_ENCODING", "Message payload cannot be decoded", err.Error(), location))
			continue
		}
		var value any
		if err := json.Unmarshal(payload, &value); err != nil {
			findings = append(findings, finding("ASYNCAPI_INVALID_JSON", "Message payload is not valid JSON", err.Error(), location))
			continue
		}
		selected, violations := v.selectMessage(record, messages, value)
		if selected == "" {
			findings = append(findings, finding(
				"ASYNCAPI_SCHEMA_VIOLATION", "Message violates every channel schema",
				fmt.Sprintf("topic %s (%s): %s", record.Topic, channelName, strings.Join(violations, "; ")), location,
			))
			continue
		}
		if record.Schema != "" && record.Schema != selected {
			findings = append(findings, finding(
				"ASYNCAPI_MESSAGE_NAME_DRIFT", "Captured message schema name differs",
				fmt.Sprintf("captured %s but contract selected %s", record.Schema, selected), location,
			))
		}
		for _, candidate := range messages {
			if candidate.name != selected || candidate.message.Headers == nil {
				continue
			}
			headerValues, err := record.HeaderValues()
			if err != nil {
				findings = append(findings, finding("ASYNCAPI_INVALID_HEADER_ENCODING", "Message headers cannot be decoded", err.Error(), location))
				break
			}
			headerObject := make(map[string]any, len(headerValues))
			for _, header := range headerValues {
				headerObject[header.Key] = string(header.Value)
			}
			var headerViolations []string
			v.validateSchema(candidate.message.Headers, headerObject, "$headers", &headerViolations, 0)
			for _, violation := range headerViolations {
				findings = append(findings, finding("ASYNCAPI_HEADER_SCHEMA_VIOLATION", "Message headers violate the AsyncAPI schema", violation, location))
			}
			break
		}
	}
	return findings
}

func (v *Validator) channel(topic string) (string, *Channel) {
	names := make([]string, 0, len(v.document.Channels))
	for name := range v.document.Channels {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		channel := v.document.Channels[name]
		address := channel.Address
		if address == "" {
			address = name
		}
		if address == topic {
			return name, &channel
		}
	}
	return "", nil
}

type namedMessage struct {
	name    string
	message *Message
}

func (v *Validator) messages(channel Channel, location string) ([]namedMessage, []reporting.Finding) {
	names := make([]string, 0, len(channel.Messages))
	for name := range channel.Messages {
		names = append(names, name)
	}
	sort.Strings(names)
	var result []namedMessage
	var findings []reporting.Finding
	for _, name := range names {
		definition := channel.Messages[name]
		resolved, err := v.resolveMessage(definition)
		if err != nil {
			findings = append(findings, finding("ASYNCAPI_INVALID_MESSAGE_REF", "Message reference cannot be resolved", err.Error(), location))
			continue
		}
		messageName := name
		if resolved.Name != "" {
			messageName = resolved.Name
		}
		result = append(result, namedMessage{name: messageName, message: resolved})
	}
	return result, findings
}

func (v *Validator) resolveMessage(definition *Message) (*Message, error) {
	return v.resolveMessageReference(definition, map[string]bool{})
}

func (v *Validator) resolveMessageReference(definition *Message, resolving map[string]bool) (*Message, error) {
	if definition == nil {
		return nil, fmt.Errorf("message definition is empty")
	}
	if definition.Ref == "" {
		return definition, nil
	}
	const prefix = "#/components/messages/"
	if !strings.HasPrefix(definition.Ref, prefix) {
		return nil, fmt.Errorf("unsupported external message reference %s", definition.Ref)
	}
	name := strings.TrimPrefix(definition.Ref, prefix)
	if resolving[name] {
		return nil, fmt.Errorf("cyclic message component reference %s", name)
	}
	resolved := v.document.Components.Messages[name]
	if resolved == nil {
		return nil, fmt.Errorf("missing message component %s", name)
	}
	resolving[name] = true
	return v.resolveMessageReference(resolved, resolving)
}

func (v *Validator) selectMessage(record message.Record, messages []namedMessage, value any) (string, []string) {
	var allViolations []string
	for _, candidate := range messages {
		if record.Schema != "" && candidate.name != record.Schema {
			continue
		}
		var violations []string
		v.validateSchema(candidate.message.Payload, value, "$", &violations, 0)
		if len(violations) == 0 {
			return candidate.name, nil
		}
		allViolations = append(allViolations, candidate.name+": "+strings.Join(violations, ", "))
	}
	if len(allViolations) == 0 && record.Schema != "" {
		allViolations = append(allViolations, "captured schema "+record.Schema+" is not declared on the channel")
	}
	return "", allViolations
}

func (v *Validator) validateSchema(schema *Schema, value any, path string, violations *[]string, depth int) {
	if schema == nil {
		return
	}
	if depth > 256 {
		*violations = append(*violations, path+" exceeds the maximum schema recursion depth")
		return
	}
	if schema.Ref != "" {
		const prefix = "#/components/schemas/"
		if !strings.HasPrefix(schema.Ref, prefix) {
			*violations = append(*violations, path+" uses unsupported external schema reference "+schema.Ref)
			return
		}
		name := strings.TrimPrefix(schema.Ref, prefix)
		resolved := v.document.Components.Schemas[name]
		if resolved == nil {
			*violations = append(*violations, path+" references missing schema "+name)
			return
		}
		v.validateSchema(resolved, value, path, violations, depth+1)
		return
	}
	expected, _ := schemaTypes(schema.Type)
	if len(expected) > 0 && !matchesAnyType(expected, value) {
		*violations = append(*violations, fmt.Sprintf("%s expected %s but received %T", path, strings.Join(expected, " or "), value))
		return
	}
	if len(schema.Enum) > 0 && !enumContains(schema.Enum, value) {
		*violations = append(*violations, path+" is not one of the permitted enum values")
	}
	if schema.Pattern != "" {
		text, ok := value.(string)
		pattern, err := regexp.Compile(schema.Pattern)
		if err != nil {
			*violations = append(*violations, path+" contract contains an invalid regex")
		} else if !ok || !pattern.MatchString(text) {
			*violations = append(*violations, path+" does not match its pattern")
		}
	}
	switch typed := value.(type) {
	case map[string]any:
		for _, required := range schema.Required {
			if _, ok := typed[required]; !ok {
				*violations = append(*violations, path+"."+required+" is required")
			}
		}
		for name, child := range typed {
			property := schema.Properties[name]
			if property != nil {
				v.validateSchema(property, child, path+"."+name, violations, depth+1)
				continue
			}
			if allowed, ok := schema.AdditionalProperties.(bool); ok && !allowed {
				*violations = append(*violations, path+"."+name+" is not allowed")
			}
		}
	case []any:
		for index, child := range typed {
			v.validateSchema(schema.Items, child, fmt.Sprintf("%s[%d]", path, index), violations, depth+1)
		}
	}
}

func schemaTypes(value any) ([]string, error) {
	valid := map[string]bool{"object": true, "array": true, "string": true, "number": true, "integer": true, "boolean": true, "null": true}
	var values []string
	switch typed := value.(type) {
	case nil:
		return nil, nil
	case string:
		values = []string{typed}
	case []any:
		for _, item := range typed {
			name, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("schema type array must contain only strings")
			}
			values = append(values, name)
		}
	default:
		return nil, fmt.Errorf("schema type must be a string or string array")
	}
	seen := make(map[string]bool)
	for _, name := range values {
		if !valid[name] {
			return nil, fmt.Errorf("unsupported schema type %q", name)
		}
		if seen[name] {
			return nil, fmt.Errorf("schema type %q is duplicated", name)
		}
		seen[name] = true
	}
	return values, nil
}

func matchesAnyType(expected []string, value any) bool {
	for _, candidate := range expected {
		if matchesType(candidate, value) {
			return true
		}
	}
	return false
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
		return ok && !math.IsInf(number, 0) && !math.IsNaN(number) && math.Trunc(number) == number
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "null":
		return value == nil
	default:
		return false
	}
}

func enumContains(values []any, target any) bool {
	targetJSON, _ := json.Marshal(target)
	for _, value := range values {
		valueJSON, _ := json.Marshal(value)
		if string(valueJSON) == string(targetJSON) {
			return true
		}
	}
	return false
}

func finding(ruleID, title, detail, location string) reporting.Finding {
	return reporting.Finding{RuleID: ruleID, Level: "error", Title: title, Message: detail, Location: location}
}
