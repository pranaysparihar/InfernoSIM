package scenario

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"infernosim/pkg/matcher"
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
	Status     int                 `yaml:"status" json:"status"`
	Headers    map[string][]string `yaml:"headers" json:"headers,omitempty"`
	Trailers   map[string][]string `yaml:"trailers" json:"trailers,omitempty"`
	GRPCStatus string              `yaml:"grpc_status" json:"grpc_status,omitempty"`
	Body       string              `yaml:"body" json:"body,omitempty"`
	BodyB64    string              `yaml:"body_base64" json:"body_base64,omitempty"`
}

type Result struct {
	Scenario string
	Step     string
	Response Response
}

// Engine maintains explicit scenario state. A single lock makes transitions
// atomic when a replay uses concurrent workers.
type Engine struct {
	configs []Config
	states  map[string]string
	mu      sync.Mutex
}

func New(configs []Config) (*Engine, error) {
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
		for j, step := range cfg.Steps {
			if step.State == "" {
				return nil, fmt.Errorf("scenario %q steps[%d].state is required", cfg.Name, j)
			}
			states[step.State] = struct{}{}
			if step.Response.Status < 100 || step.Response.Status > 599 {
				return nil, fmt.Errorf("scenario %q steps[%d].response.status must be 100..599", cfg.Name, j)
			}
			if step.Response.Body != "" && step.Response.BodyB64 != "" {
				return nil, fmt.Errorf("scenario %q steps[%d] sets both body and body_base64", cfg.Name, j)
			}
			if step.Response.BodyB64 != "" {
				if _, err := base64.StdEncoding.DecodeString(step.Response.BodyB64); err != nil {
					return nil, fmt.Errorf("scenario %q steps[%d].response.body_base64: %w", cfg.Name, j, err)
				}
			}
			if _, _, err := matcher.MatchRule(step.Match, &http.Request{
				Method: firstMethod(step.Match.Methods),
				Host:   "validation.invalid",
				URL:    mustValidationURL(),
				Header: make(http.Header),
			}, nil); err != nil {
				return nil, fmt.Errorf("scenario %q steps[%d].match: %w", cfg.Name, j, err)
			}
		}
		for j, step := range cfg.Steps {
			if step.NextState != "" {
				if _, ok := states[step.NextState]; !ok {
					return nil, fmt.Errorf("scenario %q steps[%d].next_state %q has no step", cfg.Name, j, step.NextState)
				}
			}
		}
		e.states[cfg.Name] = cfg.InitialState
	}
	return e, nil
}

func firstMethod(methods []string) string {
	if len(methods) > 0 {
		return methods[0]
	}
	return http.MethodGet
}

func mustValidationURL() *url.URL {
	u, _ := url.Parse("http://validation.invalid/")
	return u
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
	for _, cfg := range e.configs {
		current := e.states[cfg.Name]
		for _, step := range cfg.Steps {
			if step.State != current {
				continue
			}
			ok, _, err := matcher.MatchRule(step.Match, req, body)
			if err != nil || !ok {
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
