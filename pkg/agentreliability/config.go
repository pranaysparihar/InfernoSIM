// Package agentreliability provides deterministic fault exploration and
// consequence assertions for tool-using AI agents. It is protocol-aware but
// framework-neutral: callers supply recorded request/response exchanges while
// this package identifies agent operations, applies bounded mutations, and
// maintains a side-effect ledger.
package agentreliability

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"infernosim/pkg/jsonpath"
)

const (
	DefaultMaxCases    = 100
	DefaultMaxCalls    = 64
	MaximumCases       = 10_000
	MaximumCalls       = 10_000
	MaximumFaultDelay  = 60 * time.Second
	MaximumBodyBytes   = 16 * 1024 * 1024
	configurationLevel = 1
)

type Config struct {
	Version    int               `yaml:"version" json:"version"`
	Enabled    bool              `yaml:"enabled" json:"enabled"`
	Adapters   Adapters          `yaml:"adapters" json:"adapters"`
	Effects    []Effect          `yaml:"effects" json:"effects"`
	Faults     []Fault           `yaml:"faults" json:"faults"`
	Assertions []Assertion       `yaml:"assertions" json:"assertions"`
	Limits     Limits            `yaml:"limits" json:"limits"`
	Metadata   map[string]string `yaml:"metadata" json:"metadata,omitempty"`
}

type Adapters struct {
	MCP       bool              `yaml:"mcp" json:"mcp"`
	Providers []ProviderAdapter `yaml:"providers" json:"providers"`
}

type ProviderAdapter struct {
	Name      string `yaml:"name" json:"name"`
	Type      string `yaml:"type" json:"type"`
	HostRegex string `yaml:"host_regex" json:"host_regex"`
	PathRegex string `yaml:"path_regex" json:"path_regex"`
}

type Limits struct {
	MaxCases     int `yaml:"max_cases" json:"max_cases"`
	MaxCalls     int `yaml:"max_calls" json:"max_calls"`
	MaxBodyBytes int `yaml:"max_body_bytes" json:"max_body_bytes"`
}

type Selector struct {
	Kind       string `yaml:"kind" json:"kind"`
	Provider   string `yaml:"provider" json:"provider"`
	Tool       string `yaml:"tool" json:"tool"`
	Method     string `yaml:"method" json:"method"`
	HostRegex  string `yaml:"host_regex" json:"host_regex"`
	PathRegex  string `yaml:"path_regex" json:"path_regex"`
	Occurrence int    `yaml:"occurrence" json:"occurrence"`
}

type Effect struct {
	Name          string   `yaml:"name" json:"name"`
	Select        Selector `yaml:"select" json:"select"`
	Identity      []string `yaml:"identity" json:"identity"`
	DeduplicateBy []string `yaml:"deduplicate_by" json:"deduplicate_by"`
	CommitOn      string   `yaml:"commit_on" json:"commit_on"`
}

type Fault struct {
	ID                    string            `yaml:"id" json:"id"`
	Description           string            `yaml:"description" json:"description"`
	Category              string            `yaml:"category" json:"category"`
	Severity              string            `yaml:"severity" json:"severity"`
	Select                Selector          `yaml:"select" json:"select"`
	Mutations             []Mutation        `yaml:"mutations" json:"mutations"`
	Status                int               `yaml:"status" json:"status"`
	Headers               map[string]string `yaml:"headers" json:"headers"`
	Delay                 string            `yaml:"delay" json:"delay"`
	Timeout               string            `yaml:"timeout" json:"timeout"`
	Reset                 bool              `yaml:"reset" json:"reset"`
	CommittedResponseLost bool              `yaml:"committed_response_lost" json:"committed_response_lost"`
}

type Mutation struct {
	Operation string `yaml:"operation" json:"operation"`
	Path      string `yaml:"path" json:"path"`
	Value     any    `yaml:"value" json:"value,omitempty"`
	Type      string `yaml:"type" json:"type"`
	MaxBytes  int    `yaml:"max_bytes" json:"max_bytes"`
}

type Assertion struct {
	ID                string   `yaml:"id" json:"id"`
	Type              string   `yaml:"type" json:"type"`
	Effect            string   `yaml:"effect" json:"effect"`
	Tool              string   `yaml:"tool" json:"tool"`
	Max               int      `yaml:"max" json:"max"`
	VerificationTool  string   `yaml:"verification_tool" json:"verification_tool"`
	VerificationPath  string   `yaml:"verification_path" json:"verification_path,omitempty"`
	VerificationValue any      `yaml:"verification_value" json:"verification_value,omitempty"`
	Duration          string   `yaml:"duration" json:"duration"`
	Faults            []string `yaml:"faults" json:"faults,omitempty"`
	IncludeBaseline   bool     `yaml:"include_baseline" json:"include_baseline,omitempty"`
}

func (c *Config) ApplyDefaults() {
	if c.Version == 0 {
		c.Version = configurationLevel
	}
	if c.Limits.MaxCases == 0 {
		c.Limits.MaxCases = DefaultMaxCases
	}
	if c.Limits.MaxCalls == 0 {
		c.Limits.MaxCalls = DefaultMaxCalls
	}
	if c.Limits.MaxBodyBytes == 0 {
		c.Limits.MaxBodyBytes = MaximumBodyBytes
	}
	for index := range c.Effects {
		if c.Effects[index].CommitOn == "" {
			c.Effects[index].CommitOn = "request_received"
		}
	}
	for index := range c.Faults {
		if c.Faults[index].Category == "" {
			c.Faults[index].Category = "custom"
		}
		if c.Faults[index].Severity == "" {
			c.Faults[index].Severity = "medium"
		}
	}
}

func (c Config) Validate() error {
	c.ApplyDefaults()
	if c.Version != configurationLevel {
		return fmt.Errorf("agent.version must be %d", configurationLevel)
	}
	if c.Limits.MaxCases < 1 || c.Limits.MaxCases > MaximumCases {
		return fmt.Errorf("agent.limits.max_cases must be between 1 and %d", MaximumCases)
	}
	if c.Limits.MaxCalls < 1 || c.Limits.MaxCalls > MaximumCalls {
		return fmt.Errorf("agent.limits.max_calls must be between 1 and %d", MaximumCalls)
	}
	if c.Limits.MaxBodyBytes < 1 || c.Limits.MaxBodyBytes > MaximumBodyBytes {
		return fmt.Errorf("agent.limits.max_body_bytes must be between 1 and %d", MaximumBodyBytes)
	}
	providerNames := map[string]struct{}{}
	for index, provider := range c.Adapters.Providers {
		location := fmt.Sprintf("agent.adapters.providers[%d]", index)
		if provider.Name == "" {
			return fmt.Errorf("%s.name is required", location)
		}
		if _, duplicate := providerNames[provider.Name]; duplicate {
			return fmt.Errorf("%s.name %q is duplicated", location, provider.Name)
		}
		providerNames[provider.Name] = struct{}{}
		switch provider.Type {
		case "openai", "anthropic", "ollama", "generic":
		default:
			return fmt.Errorf("%s.type must be openai, anthropic, ollama, or generic", location)
		}
		if provider.HostRegex == "" && provider.PathRegex == "" {
			return fmt.Errorf("%s requires host_regex or path_regex", location)
		}
		if err := validateRegex(location+".host_regex", provider.HostRegex); err != nil {
			return err
		}
		if err := validateRegex(location+".path_regex", provider.PathRegex); err != nil {
			return err
		}
	}
	effectNames := map[string]struct{}{}
	effectTools := map[string]struct{}{}
	effectPaths := map[string]struct{}{}
	for index, effect := range c.Effects {
		location := fmt.Sprintf("agent.effects[%d]", index)
		if effect.Name == "" {
			return fmt.Errorf("%s.name is required", location)
		}
		if _, duplicate := effectNames[effect.Name]; duplicate {
			return fmt.Errorf("%s.name %q is duplicated", location, effect.Name)
		}
		effectNames[effect.Name] = struct{}{}
		if effect.Select.Tool != "" {
			effectTools[effect.Select.Tool] = struct{}{}
		}
		if effect.Select.PathRegex != "" {
			effectPaths[effect.Select.PathRegex] = struct{}{}
		}
		if effect.Select.Tool == "" && effect.Select.PathRegex == "" {
			return fmt.Errorf("%s.select requires tool or path_regex", location)
		}
		if err := validateSelector(location+".select", effect.Select); err != nil {
			return err
		}
		if effect.CommitOn != "" && effect.CommitOn != "request_received" {
			return fmt.Errorf("%s.commit_on currently supports only request_received", location)
		}
		for _, path := range append(append([]string(nil), effect.Identity...), effect.DeduplicateBy...) {
			if err := jsonpath.Validate(path); err != nil {
				return fmt.Errorf("%s JSONPath %q: %w", location, path, err)
			}
		}
	}
	faultIDs := map[string]struct{}{}
	for index, fault := range c.Faults {
		location := fmt.Sprintf("agent.faults[%d]", index)
		if fault.ID == "" || !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`).MatchString(fault.ID) {
			return fmt.Errorf("%s.id must contain 1-128 letters, digits, dots, underscores, or hyphens", location)
		}
		if _, duplicate := faultIDs[fault.ID]; duplicate {
			return fmt.Errorf("%s.id %q is duplicated", location, fault.ID)
		}
		faultIDs[fault.ID] = struct{}{}
		if !allowedFaultCategory(fault.Category) {
			return fmt.Errorf("%s.category %q is unsupported", location, fault.Category)
		}
		switch fault.Severity {
		case "low", "medium", "high", "critical":
		default:
			return fmt.Errorf("%s.severity must be low, medium, high, or critical", location)
		}
		if err := validateSelector(location+".select", fault.Select); err != nil {
			return err
		}
		if len(fault.Mutations) == 0 && fault.Status == 0 && len(fault.Headers) == 0 && fault.Delay == "" && fault.Timeout == "" && !fault.Reset && !fault.CommittedResponseLost {
			return fmt.Errorf("%s defines no fault action", location)
		}
		terminal := 0
		for _, enabled := range []bool{fault.Timeout != "", fault.Reset, fault.CommittedResponseLost} {
			if enabled {
				terminal++
			}
		}
		if terminal > 1 {
			return fmt.Errorf("%s may define only one of timeout, reset, or committed_response_lost", location)
		}
		if fault.CommittedResponseLost {
			_, toolEffect := effectTools[fault.Select.Tool]
			_, pathEffect := effectPaths[fault.Select.PathRegex]
			if (fault.Select.Tool == "" || !toolEffect) && (fault.Select.PathRegex == "" || !pathEffect) {
				return fmt.Errorf("%s committed_response_lost selector does not match a declared effect tool or path", location)
			}
		}
		if fault.Status != 0 && (fault.Status < 100 || fault.Status > 599) {
			return fmt.Errorf("%s.status must be a valid HTTP status", location)
		}
		if _, err := parseBoundedDuration(location+".delay", fault.Delay, true); err != nil {
			return err
		}
		if _, err := parseBoundedDuration(location+".timeout", fault.Timeout, false); err != nil {
			return err
		}
		for mutationIndex, mutation := range fault.Mutations {
			if err := validateMutation(fmt.Sprintf("%s.mutations[%d]", location, mutationIndex), mutation); err != nil {
				return err
			}
		}
		for name, value := range fault.Headers {
			if strings.TrimSpace(name) == "" || strings.ContainsAny(name+value, "\r\n") {
				return fmt.Errorf("%s.headers contains an invalid name or value", location)
			}
		}
	}
	assertionIDs := map[string]struct{}{}
	for index, assertion := range c.Assertions {
		location := fmt.Sprintf("agent.assertions[%d]", index)
		if assertion.ID == "" {
			return fmt.Errorf("%s.id is required", location)
		}
		if _, duplicate := assertionIDs[assertion.ID]; duplicate {
			return fmt.Errorf("%s.id %q is duplicated", location, assertion.ID)
		}
		assertionIDs[assertion.ID] = struct{}{}
		for _, faultID := range assertion.Faults {
			if _, exists := faultIDs[faultID]; !exists {
				return fmt.Errorf("%s.faults references unknown fault %q", location, faultID)
			}
		}
		switch assertion.Type {
		case "exactly_once_effect", "at_most_once_effect", "forbidden_effect":
			if _, exists := effectNames[assertion.Effect]; !exists {
				return fmt.Errorf("%s.effect %q is not declared", location, assertion.Effect)
			}
		case "max_calls":
			if assertion.Tool == "" || assertion.Max < 0 {
				return fmt.Errorf("%s max_calls requires tool and max >= 0", location)
			}
		case "require_verification_before_retry":
			if _, exists := effectNames[assertion.Effect]; !exists || assertion.VerificationTool == "" {
				return fmt.Errorf("%s requires a declared effect and verification_tool", location)
			}
			if assertion.VerificationPath != "" {
				if err := jsonpath.Validate(assertion.VerificationPath); err != nil {
					return fmt.Errorf("%s.verification_path: %w", location, err)
				}
			}
		case "no_unexpected_calls":
		case "deadline":
			if duration, err := parseBoundedDuration(location+".duration", assertion.Duration, false); err != nil || duration == 0 {
				if err != nil {
					return err
				}
				return fmt.Errorf("%s.duration is required", location)
			}
		default:
			return fmt.Errorf("%s.type %q is unsupported", location, assertion.Type)
		}
	}
	return nil
}

func validateSelector(location string, selector Selector) error {
	allowedKinds := map[string]bool{
		"": true, "any": true, "http_response": true, "mcp_tool_result": true,
		"mcp_tools_list": true, "llm_response": true,
	}
	if !allowedKinds[selector.Kind] {
		return fmt.Errorf("%s.kind %q is unsupported", location, selector.Kind)
	}
	if selector.Occurrence < 0 {
		return fmt.Errorf("%s.occurrence must be >= 0", location)
	}
	if err := validateRegex(location+".host_regex", selector.HostRegex); err != nil {
		return err
	}
	return validateRegex(location+".path_regex", selector.PathRegex)
}

func validateRegex(location, value string) error {
	if value == "" {
		return nil
	}
	if len(value) > 4096 {
		return fmt.Errorf("%s exceeds 4096 bytes", location)
	}
	if _, err := regexp.Compile(value); err != nil {
		return fmt.Errorf("%s: %w", location, err)
	}
	return nil
}

func validateMutation(location string, mutation Mutation) error {
	switch mutation.Operation {
	case "delete", "set", "change_type", "duplicate", "reverse":
		if mutation.Path == "" {
			return fmt.Errorf("%s.path is required", location)
		}
		if err := jsonpath.Validate(mutation.Path); err != nil {
			return fmt.Errorf("%s.path: %w", location, err)
		}
	case "truncate":
		if mutation.MaxBytes < 0 || mutation.MaxBytes > MaximumBodyBytes {
			return fmt.Errorf("%s.max_bytes must be between 0 and %d", location, MaximumBodyBytes)
		}
	case "empty_success":
	default:
		return fmt.Errorf("%s.operation %q is unsupported", location, mutation.Operation)
	}
	if mutation.Operation == "change_type" {
		switch mutation.Type {
		case "string", "number", "boolean", "null", "object", "array":
		default:
			return fmt.Errorf("%s.type %q is unsupported", location, mutation.Type)
		}
	}
	return nil
}

func parseBoundedDuration(location, value string, zeroAllowed bool) (time.Duration, error) {
	if value == "" {
		return 0, nil
	}
	duration, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", location, err)
	}
	if duration < 0 || (!zeroAllowed && duration == 0) || duration > MaximumFaultDelay {
		return 0, fmt.Errorf("%s must be %s and no more than %s", location, map[bool]string{true: ">= 0", false: "> 0"}[zeroAllowed], MaximumFaultDelay)
	}
	return duration, nil
}

type Case struct {
	ID          string `json:"id"`
	FaultID     string `json:"fault_id,omitempty"`
	Description string `json:"description"`
	Category    string `json:"category,omitempty"`
	Severity    string `json:"severity,omitempty"`
}

// PlanCases returns the baseline followed by stable single-fault cases. The
// caller-provided scope hash normally includes the incident and replay config.
func PlanCases(config Config, scopeHash string, seed int64, includeBaseline bool, budget int) ([]Case, error) {
	config.ApplyDefaults()
	if err := config.Validate(); err != nil {
		return nil, err
	}
	if budget == 0 || budget > config.Limits.MaxCases {
		budget = config.Limits.MaxCases
	}
	if budget < 1 {
		return nil, fmt.Errorf("case budget must be >= 1")
	}
	var cases []Case
	if includeBaseline {
		cases = append(cases, Case{ID: caseID(scopeHash, seed, "baseline"), Description: "recorded baseline"})
	}
	faultCases := make([]Case, 0, len(config.Faults))
	for _, fault := range config.Faults {
		description := fault.Description
		if description == "" {
			description = fault.ID
		}
		faultCases = append(faultCases, Case{
			ID: caseID(scopeHash, seed, fault.ID), FaultID: fault.ID, Description: description,
			Category: fault.Category, Severity: fault.Severity,
		})
	}
	// Rank by the seeded, scope-bound ID. A fixed seed is fully stable, while a
	// different seed selects a different deterministic subset when budget is
	// smaller than the configured fault inventory.
	sort.SliceStable(faultCases, func(i, j int) bool { return faultCases[i].ID < faultCases[j].ID })
	for _, plannedCase := range faultCases {
		if len(cases) >= budget {
			break
		}
		cases = append(cases, plannedCase)
	}
	return cases, nil
}

func allowedFaultCategory(value string) bool {
	switch value {
	case "transport", "rate_limit", "schema_drift", "stale_data", "ambiguous_side_effect", "event_duplication", "tool_contract", "llm_envelope", "custom":
		return true
	default:
		return false
	}
}

func caseID(scopeHash string, seed int64, faultID string) string {
	payload, _ := json.Marshal(struct {
		Scope string `json:"scope"`
		Seed  int64  `json:"seed"`
		Fault string `json:"fault"`
	}{scopeHash, seed, faultID})
	hash := sha256.Sum256(payload)
	return "fc_" + hex.EncodeToString(hash[:8])
}
