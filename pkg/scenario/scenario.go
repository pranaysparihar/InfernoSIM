package scenario

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"infernosim/pkg/grpcsim"
	"infernosim/pkg/matcher"
	"infernosim/pkg/simtemplate"
)

type Config struct {
	Name         string `yaml:"name" json:"name"`
	InitialState string `yaml:"initial_state" json:"initial_state"`
	Steps        []Step `yaml:"steps" json:"steps"`
}

type Step struct {
	Name      string       `yaml:"name" json:"name,omitempty"`
	State     string       `yaml:"state" json:"state"`
	Match     matcher.Rule `yaml:"match" json:"match"`
	NextState string       `yaml:"next_state" json:"next_state,omitempty"`
	Response  Response     `yaml:"response" json:"response"`
}

type Response struct {
	Status             int                 `yaml:"status" json:"status"`
	Headers            map[string][]string `yaml:"headers" json:"headers,omitempty"`
	Trailers           map[string][]string `yaml:"trailers" json:"trailers,omitempty"`
	GRPCStatus         string              `yaml:"grpc_status" json:"grpc_status,omitempty"`
	Body               string              `yaml:"body" json:"body,omitempty"`
	BodyB64            string              `yaml:"body_base64" json:"body_base64,omitempty"`
	BodyTemplate       string              `yaml:"body_template" json:"body_template,omitempty"`
	ProtobufType       string              `yaml:"protobuf_type" json:"protobuf_type,omitempty"`
	ProtobufJSON       string              `yaml:"protobuf_json" json:"protobuf_json,omitempty"`
	ProtobufStream     []string            `yaml:"protobuf_stream" json:"protobuf_stream,omitempty"`
	StreamMessageDelay string              `yaml:"stream_message_delay" json:"stream_message_delay,omitempty"`
}

type Result struct {
	Scenario string
	Step     string
	Response Response
}

// Engine maintains explicit scenario state. A single lock makes transitions
// atomic when a replay uses concurrent workers.
type Engine struct {
	configs  []Config
	states   map[string]string
	compiled [][]*matcher.RuleMatcher
	mu       sync.Mutex
}

func New(configs []Config) (*Engine, error) {
	return NewWithMatching(configs, matcher.Config{})
}

func NewWithMatching(configs []Config, matching matcher.Config) (*Engine, error) {
	semanticMatcher, err := matcher.New(matching)
	if err != nil {
		return nil, err
	}
	return NewWithRegistry(configs, matching, semanticMatcher.GRPCRegistry())
}

func NewWithRegistry(configs []Config, matching matcher.Config, registry *grpcsim.Registry) (*Engine, error) {
	e := &Engine{configs: configs, states: make(map[string]string)}
	names := make(map[string]struct{})
	for i, cfg := range configs {
		if strings.TrimSpace(cfg.Name) == "" {
			return nil, fmt.Errorf("scenarios[%d].name is required", i)
		}
		if _, exists := names[cfg.Name]; exists {
			return nil, fmt.Errorf("scenario name %q is duplicated", cfg.Name)
		}
		names[cfg.Name] = struct{}{}
		if cfg.InitialState == "" {
			return nil, fmt.Errorf("scenario %q initial_state is required", cfg.Name)
		}
		states := map[string]struct{}{cfg.InitialState: {}}
		compiledSteps := make([]*matcher.RuleMatcher, 0, len(cfg.Steps))
		for j, step := range cfg.Steps {
			if step.State == "" {
				return nil, fmt.Errorf("scenario %q steps[%d].state is required", cfg.Name, j)
			}
			states[step.State] = struct{}{}
			if step.Response.Status < 100 || step.Response.Status > 599 {
				return nil, fmt.Errorf("scenario %q steps[%d].response.status must be 100..599", cfg.Name, j)
			}
			responseForms := 0
			for _, present := range []bool{
				step.Response.Body != "",
				step.Response.BodyB64 != "",
				step.Response.BodyTemplate != "",
				step.Response.ProtobufJSON != "",
				len(step.Response.ProtobufStream) > 0,
			} {
				if present {
					responseForms++
				}
			}
			if responseForms > 1 {
				return nil, fmt.Errorf("scenario %q steps[%d] configures more than one response body form", cfg.Name, j)
			}
			if step.Response.BodyB64 != "" {
				if _, err := base64.StdEncoding.DecodeString(step.Response.BodyB64); err != nil {
					return nil, fmt.Errorf("scenario %q steps[%d].response.body_base64: %w", cfg.Name, j, err)
				}
			}
			if step.Response.StreamMessageDelay != "" {
				delay, err := time.ParseDuration(step.Response.StreamMessageDelay)
				if err != nil || delay < 0 {
					return nil, fmt.Errorf("scenario %q steps[%d].response.stream_message_delay must be a non-negative duration", cfg.Name, j)
				}
			}
			if err := validateResponseTemplates(step.Response); err != nil {
				return nil, fmt.Errorf("scenario %q steps[%d].response: %w", cfg.Name, j, err)
			}
			compiled, err := matcher.CompileRuleWithRegistry(step.Match, matching, registry)
			if err != nil {
				return nil, fmt.Errorf("scenario %q steps[%d].match: %w", cfg.Name, j, err)
			}
			compiledSteps = append(compiledSteps, compiled)
		}
		for j, step := range cfg.Steps {
			if step.NextState != "" {
				if _, ok := states[step.NextState]; !ok {
					return nil, fmt.Errorf("scenario %q steps[%d].next_state %q has no step", cfg.Name, j, step.NextState)
				}
			}
		}
		e.states[cfg.Name] = cfg.InitialState
		e.compiled = append(e.compiled, compiledSteps)
	}
	return e, nil
}

func validateResponseTemplates(response Response) error {
	if err := simtemplate.Validate(response.BodyTemplate); err != nil {
		return fmt.Errorf("body_template: %w", err)
	}
	for name, values := range response.Headers {
		for index, value := range values {
			if err := simtemplate.Validate(value); err != nil {
				return fmt.Errorf("headers.%s[%d]: %w", name, index, err)
			}
		}
	}
	for name, values := range response.Trailers {
		for index, value := range values {
			if err := simtemplate.Validate(value); err != nil {
				return fmt.Errorf("trailers.%s[%d]: %w", name, index, err)
			}
		}
	}
	if err := simtemplate.Validate(response.ProtobufJSON); err != nil {
		return fmt.Errorf("protobuf_json: %w", err)
	}
	for index, value := range response.ProtobufStream {
		if err := simtemplate.Validate(value); err != nil {
			return fmt.Errorf("protobuf_stream[%d]: %w", index, err)
		}
	}
	return nil
}

func (e *Engine) Reset() {
	if e == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, cfg := range e.configs {
		e.states[cfg.Name] = cfg.InitialState
	}
}

func (e *Engine) Match(req *http.Request, body []byte) (Result, bool) {
	if e == nil {
		return Result{}, false
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	for configIndex, cfg := range e.configs {
		current := e.states[cfg.Name]
		for stepIndex, step := range cfg.Steps {
			if step.State != current {
				continue
			}
			ok, _ := e.compiled[configIndex][stepIndex].Match(req, body)
			if !ok {
				continue
			}
			if step.NextState != "" {
				e.states[cfg.Name] = step.NextState
			}
			return Result{
				Scenario: cfg.Name,
				Step:     step.Name,
				Response: step.Response,
			}, true
		}
	}
	return Result{}, false
}

func (r Response) Bytes() ([]byte, error) {
	if r.BodyB64 != "" {
		return base64.StdEncoding.DecodeString(r.BodyB64)
	}
	return []byte(r.Body), nil
}
