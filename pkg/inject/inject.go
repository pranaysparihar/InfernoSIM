package inject

import (
	"fmt"
	"math/rand"
	"strconv"
	"strings"
	"time"
)

// InjectConfig models the parsed fault injection parameters.
type InjectConfig struct {
	JitterMs   int     // ± ms delay
	DropRate   float64 // 0.0 to 1.0 probability to drop connection
	ResetRate  float64 // 0.0 to 1.0 probability to reset connection
	Status     int     // Override HTTP status
	StatusRate float64 // 0.0 to 1.0 probability to apply status override
	rng        *rand.Rand
}

// ParseConfig parses a string like "jitter=50ms,drop=5%,reset=5%,status=503,rate=10%"
// and optionally accepts a seed for deterministic testing.
func ParseConfig(config string, seed int64) (*InjectConfig, error) {
	if config == "" {
		return nil, nil
	}

	cfg := &InjectConfig{}

	if seed != 0 {
		cfg.rng = rand.New(rand.NewSource(seed))
	} else {
		cfg.rng = rand.New(rand.NewSource(time.Now().UnixNano()))
	}

	parts := strings.Split(config, ",")
	for _, p := range parts {
		kv := strings.SplitN(strings.TrimSpace(p), "=", 2)
		if len(kv) != 2 {
			continue // skip malformed
		}
		key := strings.ToLower(kv[0])
		val := kv[1]

		switch key {
		case "jitter":
			val = strings.TrimSuffix(val, "ms")
			if ms, err := strconv.Atoi(val); err == nil {
				cfg.JitterMs = ms
			} else {
				return nil, fmt.Errorf("invalid jitter: %v", val)
			}
		case "drop":
			val = strings.TrimSuffix(val, "%")
			if pct, err := strconv.ParseFloat(val, 64); err == nil {
				cfg.DropRate = pct / 100.0
			} else {
				return nil, fmt.Errorf("invalid drop rate: %v", val)
			}
		case "reset":
			val = strings.TrimSuffix(val, "%")
			if pct, err := strconv.ParseFloat(val, 64); err == nil {
				cfg.ResetRate = pct / 100.0
			} else {
				return nil, fmt.Errorf("invalid reset rate: %v", val)
			}
		case "status":
			if status, err := strconv.Atoi(val); err == nil {
				cfg.Status = status
			} else {
				return nil, fmt.Errorf("invalid status: %v", val)
			}
		case "rate":
			val = strings.TrimSuffix(val, "%")
			if pct, err := strconv.ParseFloat(val, 64); err == nil {
				cfg.StatusRate = pct / 100.0
			} else {
				return nil, fmt.Errorf("invalid status rate: %v", val)
			}
		}
	}
	return cfg, nil
}

// Action represents an action to execute
type Action struct {
	Delay   time.Duration
	Drop    bool
	Reset   bool
	Status  int
	Applied string
}

// Evaluate determines what actions to apply to a connection or request.
// Can specify isHTTP context.
func (cfg *InjectConfig) Evaluate(isHTTP bool) Action {
	act := Action{}
	if cfg == nil {
		return act
	}

	applied := []string{}

	// Evaluate Jitter
	if cfg.JitterMs > 0 {
		// ± jitter
		jitter := cfg.rng.Intn(cfg.JitterMs*2) - cfg.JitterMs
		if jitter != 0 {
			act.Delay = time.Duration(jitter) * time.Millisecond
			applied = append(applied, fmt.Sprintf("jitter=%dms", jitter))
		}
	}

	// Evaluate Drop
	if cfg.DropRate > 0 && cfg.rng.Float64() < cfg.DropRate {
		act.Drop = true
		applied = append(applied, "drop")
	}

	// Evaluate Reset
	if cfg.ResetRate > 0 && cfg.rng.Float64() < cfg.ResetRate {
		act.Reset = true
		applied = append(applied, "reset")
	}

	// Evaluate HTTP Status
	if isHTTP && cfg.Status > 0 && cfg.StatusRate > 0 {
		if cfg.rng.Float64() < cfg.StatusRate {
			act.Status = cfg.Status
			applied = append(applied, fmt.Sprintf("status=%d", cfg.Status))
		}
	}

	if len(applied) > 0 {
		act.Applied = strings.Join(applied, ",")
	}
	return act
}
