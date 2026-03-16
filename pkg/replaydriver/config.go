package replaydriver

import (
	"fmt"
	"os"
	"time"

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
	Target    string      `yaml:"target"`
	TimeScale float64     `yaml:"time_scale"`
	Runs      int         `yaml:"runs"`
	SafeMode  bool        `yaml:"safe_mode"`
	Chaos     ChaosConfig `yaml:"chaos"`
	State     StateConfig `yaml:"state"`
}

// ChaosConfig defines fault injection settings.
type ChaosConfig struct {
	Latency LatencyConfig `yaml:"latency"`
}

// LatencyConfig injects artificial latency into replayed requests.
type LatencyConfig struct {
	// Request is the 1-based index of the request to affect. 0 means all requests.
	Request int    `yaml:"request"`
	// Delay is the duration string to add (e.g. "500ms", "1s").
	Delay   string `yaml:"delay"`
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
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return ReplayYAMLConfig{}, fmt.Errorf("parse replay config %q: %w", path, err)
	}
	return cfg, nil
}

// ChaosDelay parses the Latency.Delay string into a time.Duration.
// Returns 0 if empty or unparseable.
func (c ChaosConfig) ChaosDelay() time.Duration {
	if c.Latency.Delay == "" {
		return 0
	}
	d, err := time.ParseDuration(c.Latency.Delay)
	if err != nil {
		return 0
	}
	return d
}
