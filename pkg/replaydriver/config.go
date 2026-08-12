package replaydriver

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"infernosim/pkg/matcher"
	"infernosim/pkg/scenario"
	"infernosim/pkg/simtemplate"
	"infernosim/pkg/workflow"

	"gopkg.in/yaml.v3"
)

// ReplayYAMLConfig is the schema for replay.yaml inside an incident bundle.
// All fields are optional; zero values mean "use the CLI flag default".
//
// Example:
//
//	target: http://staging-api
//	time_scale: 1.0
//	runs: 5
//	safe_mode: true
//	chaos:
//	  latency:
//	    request: 0     # 0 = apply to all requests
//	    delay: 500ms
//	state:
//	  file: ./state.json
type ReplayYAMLConfig struct {
	Target    string             `yaml:"target"`
	TimeScale float64            `yaml:"time_scale"`
	Runs      int                `yaml:"runs"`
	SafeMode  bool               `yaml:"safe_mode"`
	Chaos     ChaosConfig        `yaml:"chaos"`
	State     StateConfig        `yaml:"state"`
	Matching  matcher.Config     `yaml:"matching"`
	Scenarios []scenario.Config  `yaml:"scenarios"`
	Templates simtemplate.Config `yaml:"templates"`
	Stub      StubConfig         `yaml:"stub"`
	Workflows []workflow.Config  `yaml:"workflows"`
}

type StubConfig struct {
	HTTPS HTTPSStubConfig `yaml:"https"`
}

type HTTPSStubConfig struct {
	Enabled    bool     `yaml:"enabled"`
	CADir      string   `yaml:"ca_dir"`
	AllowHosts []string `yaml:"allow_hosts"`
}

// ChaosConfig defines fault injection settings.
type ChaosConfig struct {
	Latency LatencyConfig `yaml:"latency"`
}

// LatencyConfig injects artificial latency into replayed requests.
type LatencyConfig struct {
	// Request is the 1-based index of the request to affect. 0 means all requests.
	Request int `yaml:"request"`
	// Delay is the duration string to add (e.g. "500ms", "1s").
	Delay string `yaml:"delay"`
}

// StateConfig points to an external state snapshot file.
type StateConfig struct {
	// File is a path to a JSON {"old_value": "new_value"} map.
	File string `yaml:"file"`
}

// LoadReplayConfig parses a replay.yaml file.
func LoadReplayConfig(path string) (ReplayYAMLConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return ReplayYAMLConfig{}, fmt.Errorf("load replay config %q: %w", path, err)
	}
	var cfg ReplayYAMLConfig
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		return ReplayYAMLConfig{}, fmt.Errorf("parse replay config %q: %w", path, err)
	}
	if cfg.Runs < 0 {
		return ReplayYAMLConfig{}, fmt.Errorf("parse replay config %q: runs must be >= 0", path)
	}
	if cfg.TimeScale < 0 {
		return ReplayYAMLConfig{}, fmt.Errorf("parse replay config %q: time_scale must be >= 0", path)
	}
	if cfg.Chaos.Latency.Request < 0 {
		return ReplayYAMLConfig{}, fmt.Errorf("parse replay config %q: chaos.latency.request must be >= 0", path)
	}
	if _, err := cfg.Chaos.ChaosDelay(); err != nil {
		return ReplayYAMLConfig{}, fmt.Errorf("parse replay config %q: %w", path, err)
	}
	cfg.Matching.GRPC.ResolvePaths(filepath.Dir(path))
	semanticMatcher, err := matcher.New(cfg.Matching)
	if err != nil {
		return ReplayYAMLConfig{}, fmt.Errorf("parse replay config %q: %w", path, err)
	}
	if _, err := scenario.NewWithRegistry(cfg.Scenarios, cfg.Matching, semanticMatcher.GRPCRegistry()); err != nil {
		return ReplayYAMLConfig{}, fmt.Errorf("parse replay config %q: %w", path, err)
	}
	if _, err := simtemplate.New(cfg.Templates); err != nil {
		return ReplayYAMLConfig{}, fmt.Errorf("parse replay config %q: %w", path, err)
	}
	if err := workflow.ValidateConfigs(cfg.Workflows); err != nil {
		return ReplayYAMLConfig{}, fmt.Errorf("parse replay config %q: %w", path, err)
	}
	return cfg, nil
}

// ChaosDelay parses the Latency.Delay string into a time.Duration.
func (c ChaosConfig) ChaosDelay() (time.Duration, error) {
	if c.Latency.Delay == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(c.Latency.Delay)
	if err != nil {
		return 0, fmt.Errorf("invalid chaos.latency.delay %q: %w", c.Latency.Delay, err)
	}
	if d < 0 {
		return 0, fmt.Errorf("chaos.latency.delay must be >= 0")
	}
	return d, nil
}
