package simlint

import (
	"fmt"
	"sort"
	"strings"

	"infernosim/pkg/matcher"
	"infernosim/pkg/replaydriver"
)

type Diagnostic struct {
	Level    string `json:"level"`
	Code     string `json:"code"`
	Location string `json:"location"`
	Message  string `json:"message"`
}

type Result struct {
	Config      replaydriver.ReplayYAMLConfig `json:"-"`
	Diagnostics []Diagnostic                  `json:"diagnostics"`
}

func (r Result) ErrorCount() int {
	count := 0
	for _, diagnostic := range r.Diagnostics {
		if diagnostic.Level == "error" {
			count++
		}
	}
	return count
}

func (r Result) WarningCount() int {
	count := 0
	for _, diagnostic := range r.Diagnostics {
		if diagnostic.Level == "warning" {
			count++
		}
	}
	return count
}

func File(path string) (Result, error) {
	config, err := replaydriver.LoadReplayConfig(path)
	if err != nil {
		return Result{
			Diagnostics: []Diagnostic{{
				Level:    "error",
				Code:     "CONFIG_INVALID",
				Location: path,
				Message:  err.Error(),
			}},
		}, nil
	}
	result := Result{Config: config}
	result.Diagnostics = append(result.Diagnostics, lintRules(config.Matching)...)
	for index, scenario := range config.Scenarios {
		location := fmt.Sprintf("scenarios[%d]", index)
		if len(scenario.Steps) == 0 {
			result.Diagnostics = append(result.Diagnostics, warning("SCENARIO_EMPTY", location, "scenario has no steps"))
			continue
		}
		reachable := map[string]bool{scenario.InitialState: true}
		changed := true
		for changed {
			changed = false
			for _, step := range scenario.Steps {
				if reachable[step.State] && step.NextState != "" && !reachable[step.NextState] {
					reachable[step.NextState] = true
					changed = true
				}
			}
		}
		seenNames := make(map[string]bool)
		seenSelectors := make(map[string]int)
		for stepIndex, step := range scenario.Steps {
			stepLocation := fmt.Sprintf("%s.steps[%d]", location, stepIndex)
			if step.Name == "" {
				result.Diagnostics = append(result.Diagnostics, warning("STEP_UNNAMED", stepLocation, "name the step so reports and explain output are actionable"))
			} else if seenNames[step.Name] {
				result.Diagnostics = append(result.Diagnostics, diagnostic("error", "STEP_DUPLICATE_NAME", stepLocation, fmt.Sprintf("step name %q is duplicated", step.Name)))
			}
			seenNames[step.Name] = true
			if !reachable[step.State] {
				result.Diagnostics = append(result.Diagnostics, warning("STATE_UNREACHABLE", stepLocation, fmt.Sprintf("state %q cannot be reached from initial_state %q", step.State, scenario.InitialState)))
			}
			selector := selectorKey(step.Match)
			shadowKey := step.State + "\x00" + selector
			if previous, ok := seenSelectors[shadowKey]; ok {
				result.Diagnostics = append(result.Diagnostics, warning(
					"STEP_SHADOWED",
					stepLocation,
					fmt.Sprintf("same-state matcher is shadowed by steps[%d]", previous),
				))
			} else {
				seenSelectors[shadowKey] = stepIndex
			}
			if (step.Response.ProtobufJSON != "" || len(step.Response.ProtobufStream) > 0) && config.Matching.GRPC.Empty() {
				result.Diagnostics = append(result.Diagnostics, diagnostic("error", "PROTOBUF_SCHEMA_REQUIRED", stepLocation+".response", "Protobuf response synthesis requires matching.grpc.proto_files or descriptor_sets"))
			}
		}
	}
	if len(config.Scenarios) == 0 {
		result.Diagnostics = append(result.Diagnostics, warning("NO_SCENARIOS", "scenarios", "configuration defines no explicit scenarios"))
	}
	sort.SliceStable(result.Diagnostics, func(i, j int) bool {
		if result.Diagnostics[i].Level != result.Diagnostics[j].Level {
			return result.Diagnostics[i].Level == "error"
		}
		return result.Diagnostics[i].Location < result.Diagnostics[j].Location
	})
	return result, nil
}

func lintRules(config matcher.Config) []Diagnostic {
	var diagnostics []Diagnostic
	names := make(map[string]bool)
	for index, rule := range config.Rules {
		location := fmt.Sprintf("matching.rules[%d]", index)
		if rule.Name == "" {
			diagnostics = append(diagnostics, warning("RULE_UNNAMED", location, "name the rule so explain output is actionable"))
		} else if names[rule.Name] {
			diagnostics = append(diagnostics, diagnostic("error", "RULE_DUPLICATE_NAME", location, fmt.Sprintf("rule name %q is duplicated", rule.Name)))
		}
		names[rule.Name] = true
		if selectorKey(rule) == "" {
			diagnostics = append(diagnostics, warning("RULE_GLOBAL", location, "rule has no method, host, path, or gRPC selector and may match every request"))
		}
	}
	return diagnostics
}

func selectorKey(rule matcher.Rule) string {
	return strings.Join([]string{
		strings.Join(rule.Methods, ","),
		rule.HostRegex,
		rule.PathRegex,
		rule.GRPCMethod,
	}, "|")
}

func warning(code, location, message string) Diagnostic {
	return diagnostic("warning", code, location, message)
}

func diagnostic(level, code, location, message string) Diagnostic {
	return Diagnostic{Level: level, Code: code, Location: location, Message: message}
}
