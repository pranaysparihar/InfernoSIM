package inject

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

type Rule struct {
	Dep         string
	AddLatency  time.Duration // +200ms
	Timeout     time.Duration // 50ms -> force timeout error
	RetryLimit  int           // effective limit by forcing success/failure patterns
}

func ParseRules(flags []string) ([]Rule, error) {
	var rules []Rule
	for _, raw := range flags {
		// format: dep=redis latency=+200ms timeout=50ms retries=2
		parts := strings.Fields(raw)
		r := Rule{RetryLimit: -1}
		for _, p := range parts {
			kv := strings.SplitN(p, "=", 2)
			if len(kv) != 2 {
				return nil, fmt.Errorf("bad inject token: %q", p)
			}
			k, v := kv[0], kv[1]
			switch k {
			case "dep":
				r.Dep = v
			case "latency":
				v = strings.TrimPrefix(v, "+")
				d, err := time.ParseDuration(v)
				if err != nil {
					return nil, fmt.Errorf("bad latency: %q", v)
				}
				r.AddLatency = d
			case "timeout":
				d, err := time.ParseDuration(v)
				if err != nil {
					return nil, fmt.Errorf("bad timeout: %q", v)
				}
				r.Timeout = d
			case "retries":
				n, err := strconv.Atoi(v)
				if err != nil || n < 0 {
					return nil, fmt.Errorf("bad retries: %q", v)
				}
				r.RetryLimit = n
			default:
				return nil, fmt.Errorf("unknown inject key: %q", k)
			}
		}
		if r.Dep == "" {
			return nil, fmt.Errorf("inject rule missing dep=...")
		}
		rules = append(rules, r)
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