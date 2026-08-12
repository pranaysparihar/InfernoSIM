package workflow

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"infernosim/pkg/event"
	"infernosim/pkg/message"
	"infernosim/pkg/reporting"
)

type Config struct {
	Name        string `yaml:"name" json:"name"`
	Correlation string `yaml:"correlation" json:"correlation,omitempty"`
	Steps       []Step `yaml:"steps" json:"steps"`
}

type Step struct {
	Name       string `yaml:"name" json:"name"`
	Protocol   string `yaml:"protocol" json:"protocol"`
	Direction  string `yaml:"direction" json:"direction,omitempty"`
	Method     string `yaml:"method" json:"method,omitempty"`
	PathRegex  string `yaml:"path_regex" json:"path_regex,omitempty"`
	GRPCMethod string `yaml:"grpc_method" json:"grpc_method,omitempty"`
	Topic      string `yaml:"topic" json:"topic,omitempty"`
	Within     string `yaml:"within" json:"within,omitempty"`
}

type observed struct {
	protocol    string
	direction   string
	method      string
	path        string
	grpcMethod  string
	topic       string
	correlation string
	timestamp   time.Time
	sequence    int64
	location    string
}

type compiledStep struct {
	step Step
	path *regexp.Regexp
	max  time.Duration
}

func ValidateConfigs(configs []Config) error {
	names := make(map[string]bool)
	for configIndex, config := range configs {
		if strings.TrimSpace(config.Name) == "" {
			return fmt.Errorf("workflows[%d].name is required", configIndex)
		}
		if names[config.Name] {
			return fmt.Errorf("workflow name %q is duplicated", config.Name)
		}
		names[config.Name] = true
		switch config.Correlation {
		case "", "none", "prefer", "required":
		default:
			return fmt.Errorf("workflow %q correlation must be none, prefer, or required", config.Name)
		}
		if len(config.Steps) == 0 {
			return fmt.Errorf("workflow %q requires at least one step", config.Name)
		}
		stepNames := make(map[string]bool)
		for stepIndex, step := range config.Steps {
			if step.Name == "" || stepNames[step.Name] {
				return fmt.Errorf("workflow %q steps[%d] requires a unique name", config.Name, stepIndex)
			}
			stepNames[step.Name] = true
			switch step.Protocol {
			case "http":
				if step.PathRegex == "" {
					return fmt.Errorf("workflow %q step %q HTTP path_regex is required", config.Name, step.Name)
				}
			case "grpc":
				if step.GRPCMethod == "" {
					return fmt.Errorf("workflow %q step %q grpc_method is required", config.Name, step.Name)
				}
			case "kafka":
				if step.Topic == "" {
					return fmt.Errorf("workflow %q step %q Kafka topic is required", config.Name, step.Name)
				}
			default:
				return fmt.Errorf("workflow %q step %q protocol must be http, grpc, or kafka", config.Name, step.Name)
			}
			if step.Direction != "" && step.Direction != "inbound" && step.Direction != "outbound" {
				return fmt.Errorf("workflow %q step %q direction must be inbound or outbound", config.Name, step.Name)
			}
			if step.PathRegex != "" {
				if _, err := regexp.Compile(step.PathRegex); err != nil {
					return fmt.Errorf("workflow %q step %q path_regex: %w", config.Name, step.Name, err)
				}
			}
			if step.Within != "" {
				duration, err := time.ParseDuration(step.Within)
				if err != nil || duration < 0 {
					return fmt.Errorf("workflow %q step %q within must be a non-negative duration", config.Name, step.Name)
				}
			}
		}
	}
	return nil
}

func VerifyIncident(incidentDir string, configs []Config) ([]reporting.Finding, error) {
	if err := ValidateConfigs(configs); err != nil {
		return nil, err
	}
	observations, err := loadObserved(incidentDir)
	if err != nil {
		return nil, err
	}
	var findings []reporting.Finding
	for _, config := range configs {
		compiled := make([]compiledStep, 0, len(config.Steps))
		for _, step := range config.Steps {
			entry := compiledStep{step: step}
			if step.PathRegex != "" {
				entry.path = regexp.MustCompile(step.PathRegex)
			}
			if step.Within != "" {
				entry.max, _ = time.ParseDuration(step.Within)
			}
			compiled = append(compiled, entry)
		}
		cursor := 0
		correlation := ""
		previous := time.Time{}
		for _, expected := range compiled {
			matchedIndex := -1
			var correlationProblem string
			for index := cursor; index < len(observations); index++ {
				candidate := observations[index]
				if !expected.matches(candidate) {
					continue
				}
				if config.Correlation == "required" {
					if candidate.correlation == "" {
						correlationProblem = "matched event has no correlation ID"
						continue
					}
					if correlation != "" && candidate.correlation != correlation {
						correlationProblem = "matched event has a different correlation ID"
						continue
					}
				}
				if config.Correlation == "prefer" && correlation != "" && candidate.correlation != "" && candidate.correlation != correlation {
					continue
				}
				matchedIndex = index
				break
			}
			if matchedIndex < 0 {
				detail := fmt.Sprintf("expected %s step %s after workflow position %d", expected.step.Protocol, expected.step.Name, cursor)
				if correlationProblem != "" {
					detail += ": " + correlationProblem
				}
				findings = append(findings, finding("WORKFLOW_MISSING_STEP", "Causal workflow step is missing", detail, config.Name+"/"+expected.step.Name))
				break
			}
			matched := observations[matchedIndex]
			if !previous.IsZero() && expected.max > 0 && matched.timestamp.Sub(previous) > expected.max {
				findings = append(findings, finding(
					"WORKFLOW_TIMING_DRIFT", "Causal workflow exceeded its timing bound",
					fmt.Sprintf("step %s took %s after the prior step; maximum is %s", expected.step.Name, matched.timestamp.Sub(previous), expected.max),
					matched.location,
				))
			}
			if correlation == "" && matched.correlation != "" {
				correlation = matched.correlation
			}
			previous = matched.timestamp
			cursor = matchedIndex + 1
		}
	}
	return findings, nil
}

func (step compiledStep) matches(candidate observed) bool {
	if candidate.protocol != step.step.Protocol {
		return false
	}
	if step.step.Direction != "" && candidate.direction != step.step.Direction {
		return false
	}
	if step.step.Method != "" && !strings.EqualFold(candidate.method, step.step.Method) {
		return false
	}
	if step.path != nil && !step.path.MatchString(candidate.path) {
		return false
	}
	if step.step.GRPCMethod != "" && candidate.grpcMethod != step.step.GRPCMethod {
		return false
	}
	return step.step.Topic == "" || candidate.topic == step.step.Topic
}

func loadObserved(incidentDir string) ([]observed, error) {
	var result []observed
	for _, log := range []struct {
		name      string
		direction string
	}{
		{"inbound.log", "inbound"},
		{"outbound.log", "outbound"},
	} {
		path := filepath.Join(incidentDir, log.name)
		file, err := os.Open(path)
		if err != nil {
			if os.IsNotExist(err) && log.name == "outbound.log" {
				continue
			}
			return nil, err
		}
		decoder := json.NewDecoder(file)
		index := 0
		for {
			var captured event.Event
			if err := decoder.Decode(&captured); err != nil {
				if err == io.EOF {
					break
				}
				_ = file.Close()
				return nil, err
			}
			index++
			if captured.Type != "InboundRequest" && captured.Type != "OutboundCall" {
				continue
			}
			parsed, _ := url.Parse(captured.URL)
			protocol := "http"
			grpcMethod := captured.GrpcServiceMethod
			if grpcMethod == "" && strings.HasPrefix(strings.ToLower(firstHeader(captured.Headers, "Content-Type")), "application/grpc") {
				grpcMethod = parsed.Path
			}
			if grpcMethod != "" {
				protocol = "grpc"
			}
			result = append(result, observed{
				protocol: protocol, direction: log.direction, method: captured.Method, path: parsed.Path,
				grpcMethod: grpcMethod, correlation: eventCorrelation(captured), timestamp: captured.Timestamp,
				sequence: captured.Sequence, location: fmt.Sprintf("%s#event-%d", log.name, index),
			})
		}
		_ = file.Close()
	}
	messagePath := filepath.Join(incidentDir, "messages.log")
	records, err := message.LoadIfExists(messagePath)
	if err != nil {
		return nil, err
	}
	for index, record := range records {
		direction := "outbound"
		if record.Direction == "consume" {
			direction = "inbound"
		}
		result = append(result, observed{
			protocol: "kafka", direction: direction, topic: record.Topic, correlation: record.CorrelationID,
			timestamp: record.Timestamp, sequence: record.Sequence, location: fmt.Sprintf("messages.log#message-%d", index+1),
		})
	}
	sort.SliceStable(result, func(i, j int) bool {
		if result[i].timestamp.Equal(result[j].timestamp) {
			if result[i].sequence == result[j].sequence {
				return result[i].location < result[j].location
			}
			return result[i].sequence < result[j].sequence
		}
		return result[i].timestamp.Before(result[j].timestamp)
	})
	return result, nil
}

func eventCorrelation(captured event.Event) string {
	if captured.TraceID != "" {
		return captured.TraceID
	}
	for _, name := range []string{"X-Correlation-Id", "Correlation-Id", "X-Request-Id", "Traceparent"} {
		if value := firstHeader(captured.Headers, name); value != "" {
			return value
		}
	}
	return ""
}

func firstHeader(headers map[string][]string, wanted string) string {
	for name, values := range headers {
		if strings.EqualFold(name, wanted) && len(values) > 0 {
			return values[0]
		}
	}
	return ""
}

func finding(ruleID, title, detail, location string) reporting.Finding {
	return reporting.Finding{RuleID: ruleID, Level: "error", Title: title, Message: detail, Location: location}
}
