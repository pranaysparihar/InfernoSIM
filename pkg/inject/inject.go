package inject

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

type Rule struct {
	Dep        string
	AddLatency time.Duration // +200ms
	Timeout    time.Duration // 50ms -> force timeout error
	RetryLimit int           // effective limit by forcing success/failure patterns
}

type ValidationError struct {
	SupportedKeys   []string
	UnsupportedKeys []string
	Reason          string
}

func (e *ValidationError) Error() string {
	if e.Reason != "" {
		return e.Reason
	}
	return "invalid inject rule"
}

func supportedKeysForOutput() []string {
	// Keep this list aligned with CLI messaging requirements.
	return []string{"latency", "timeout"}
}

func ParseRules(flags []string) ([]Rule, error) {
	var rules []Rule
	unsupported := map[string]struct{}{}
	for _, raw := range flags {
		// format: dep=redis latency=+200ms timeout=50ms retries=2
		parts := strings.Fields(raw)
		r := Rule{RetryLimit: -1}
		for _, p := range parts {
			kv := strings.SplitN(p, "=", 2)
			if len(kv) != 2 {
				return nil, &ValidationError{
					SupportedKeys: supportedKeysForOutput(),
					Reason:        fmt.Sprintf("bad inject token: %q", p),
				}
			}
			k, v := kv[0], kv[1]
			switch k {
			case "dep":
				r.Dep = v
			case "latency":
				v = strings.TrimPrefix(v, "+")
				d, err := time.ParseDuration(v)
				if err != nil {
					return nil, &ValidationError{
						SupportedKeys: supportedKeysForOutput(),
						Reason:        fmt.Sprintf("bad latency: %q", v),
					}
				}
				r.AddLatency = d
			case "timeout":
				d, err := time.ParseDuration(v)
				if err != nil {
					return nil, &ValidationError{
						SupportedKeys: supportedKeysForOutput(),
						Reason:        fmt.Sprintf("bad timeout: %q", v),
					}
				}
				r.Timeout = d
			case "retries":
				n, err := strconv.Atoi(v)
				if err != nil || n < 0 {
					return nil, &ValidationError{
						SupportedKeys: supportedKeysForOutput(),
						Reason:        fmt.Sprintf("bad retries: %q", v),
					}
				}
				r.RetryLimit = n
			default:
				unsupported[k] = struct{}{}
			}
		}
		if r.Dep == "" {
			return nil, &ValidationError{
				SupportedKeys: supportedKeysForOutput(),
				Reason:        "inject rule missing dep=...",
			}
		}
		rules = append(rules, r)
	}
	if len(unsupported) > 0 {
		keys := make([]string, 0, len(unsupported))
		for k := range unsupported {
			keys = append(keys, k)
		}
		return nil, &ValidationError{
			SupportedKeys:   supportedKeysForOutput(),
			UnsupportedKeys: keys,
			Reason:          fmt.Sprintf("unsupported inject keys: %s", strings.Join(keys, ", ")),
		}
	}
	return rules, nil
}

func Match(dep string, rules []Rule) *Rule {
	for i := range rules {
		if rules[i].Dep == dep {
			return &rules[i]
		}
	}
	return nil
}
