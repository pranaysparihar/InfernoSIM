// Package agentrunner executes an explicitly supplied agent command inside an
// InfernoSIM recorded dependency universe and evaluates agent-reliability
// assertions. Incident configuration is data only and is never executable.
package agentrunner

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"infernosim/pkg/agentreliability"
	"infernosim/pkg/replaydriver"
	"infernosim/pkg/reporting"
	"infernosim/pkg/simserver"
)

const (
	DefaultTimeout     = 2 * time.Minute
	MaximumTimeout     = 10 * time.Minute
	DefaultOutputLimit = 1024 * 1024
)

type Options struct {
	IncidentDir      string
	ConfigPath       string
	Case             agentreliability.Case
	Command          []string
	Timeout          time.Duration
	Environment      []string
	OutputLimit      int
	RestartAfterCall int
	StateCheck       StateCheckOptions
	AgentConfig      *agentreliability.Config
}

type ProcessResult struct {
	ExitCode        int    `json:"exit_code"`
	Stdout          string `json:"stdout,omitempty"`
	Stderr          string `json:"stderr,omitempty"`
	OutputTruncated bool   `json:"output_truncated"`
	TimedOut        bool   `json:"timed_out"`
	Restarted       bool   `json:"restarted"`
}

type Result struct {
	Version          int                                `json:"version"`
	Case             agentreliability.Case              `json:"case"`
	Passed           bool                               `json:"passed"`
	Duration         time.Duration                      `json:"duration"`
	Process          ProcessResult                      `json:"process"`
	Simulator        any                                `json:"simulator"`
	Assertions       []agentreliability.AssertionResult `json:"assertions"`
	Failure          string                             `json:"failure,omitempty"`
	ScopeHash        string                             `json:"scope_hash,omitempty"`
	RestartAfterCall int                                `json:"restart_after_call,omitempty"`
	InvalidRun       bool                               `json:"invalid_run,omitempty"`
	StateCheckHash   string                             `json:"state_check_hash,omitempty"`
}

type Prepared struct {
	Config     replaydriver.ReplayYAMLConfig
	ConfigPath string
	ScopeHash  string
	Cases      []agentreliability.Case
}

type SurfaceCategory struct {
	Category         string  `json:"category"`
	Total            int     `json:"total"`
	Passed           int     `json:"passed"`
	Failed           int     `json:"failed"`
	ObservedPassRate float64 `json:"observed_pass_rate"`
}

type Surface struct {
	Version          int               `json:"version"`
	FaultCases       int               `json:"fault_cases"`
	FaultsPassed     int               `json:"faults_passed"`
	FaultsFailed     int               `json:"faults_failed"`
	ObservedPassRate float64           `json:"observed_pass_rate"`
	BaselinePassed   bool              `json:"baseline_passed"`
	Categories       []SurfaceCategory `json:"categories"`
}

func Prepare(incidentDir, configPath string, seed int64, budget int, includeBaseline bool) (Prepared, error) {
	bundle, err := replaydriver.OpenBundle(incidentDir)
	if err != nil {
		return Prepared{}, err
	}
	if configPath == "" {
		configPath = bundle.ConfigPath
	}
	if _, err := os.Stat(configPath); err != nil {
		return Prepared{}, fmt.Errorf("agent reliability requires replay configuration %q: %w", configPath, err)
	}
	config, err := replaydriver.LoadReplayConfig(configPath)
	if err != nil {
		return Prepared{}, err
	}
	if !config.Agent.Enabled {
		return Prepared{}, fmt.Errorf("agent reliability requires agent.enabled: true")
	}
	scopeHash, err := hashScope(bundle, configPath)
	if err != nil {
		return Prepared{}, err
	}
	cases, err := agentreliability.PlanCases(config.Agent, scopeHash, seed, includeBaseline, budget)
	if err != nil {
		return Prepared{}, err
	}
	return Prepared{Config: config, ConfigPath: configPath, ScopeHash: scopeHash, Cases: cases}, nil
}

func ResolveCase(cases []agentreliability.Case, value string) (agentreliability.Case, error) {
	if value == "" || value == "baseline" {
		for _, candidate := range cases {
			if candidate.Baseline() {
				return candidate, nil
			}
		}
		return agentreliability.Case{}, fmt.Errorf("baseline case was not planned")
	}
	for _, candidate := range cases {
		if candidate.ID == value || candidate.FaultID == value {
			return candidate, nil
		}
	}
	return agentreliability.Case{}, fmt.Errorf("unknown case or fault %q", value)
}

func Run(ctx context.Context, opts Options) (Result, error) {
	if err := opts.StateCheck.Validate(); err != nil {
		return Result{}, err
	}
	if len(opts.Command) == 0 || strings.TrimSpace(opts.Command[0]) == "" {
		return Result{}, fmt.Errorf("an explicit agent command is required after --")
	}
	if opts.Timeout == 0 {
		opts.Timeout = DefaultTimeout
	}
	if opts.Timeout < time.Millisecond || opts.Timeout > MaximumTimeout {
		return Result{}, fmt.Errorf("timeout must be between 1ms and %s", MaximumTimeout)
	}
	if opts.OutputLimit == 0 {
		opts.OutputLimit = DefaultOutputLimit
	}
	if opts.OutputLimit < 1024 || opts.OutputLimit > 16*1024*1024 {
		return Result{}, fmt.Errorf("output limit must be between 1 KiB and 16 MiB")
	}
	if opts.RestartAfterCall < 0 || opts.RestartAfterCall > agentreliability.MaximumCalls {
		return Result{}, fmt.Errorf("restart-after-call must be between 0 and %d", agentreliability.MaximumCalls)
	}
	configPath := opts.ConfigPath
	prepared, err := Prepare(opts.IncidentDir, configPath, 0, 1+agentreliability.MaximumCases, true)
	if err != nil {
		return Result{}, err
	}
	if opts.AgentConfig != nil {
		prepared.Config.Agent = *opts.AgentConfig
		prepared.Config.Agent.ApplyDefaults()
		if err := prepared.Config.Agent.Validate(); err != nil {
			return Result{}, err
		}
		prepared.ScopeHash = agentreliability.StableHash(struct {
			Base  string
			Agent agentreliability.Config
		}{prepared.ScopeHash, prepared.Config.Agent})
	}
	if len(opts.StateCheck.Command) > 0 {
		for _, a := range prepared.Config.Agent.Assertions {
			if strings.HasPrefix(a.ID, "state:") {
				return Result{}, fmt.Errorf("state: assertion prefix is reserved for independent checks")
			}
		}
	}
	if opts.Case.ID == "" {
		opts.Case, err = ResolveCase(prepared.Cases, "baseline")
		if err != nil {
			return Result{}, err
		}
	}
	runContext, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()
	boundary := make(chan struct{}, 1)
	resume := make(chan struct{})
	server, err := simserver.New(simserver.Options{
		IncidentDir: opts.IncidentDir, ConfigPath: prepared.ConfigPath,
		Listen: "127.0.0.1:0", AdminListen: "127.0.0.1:0",
		AgentFaultIDs: faultIDs(opts.Case), AgentCaseID: opts.Case.ID,
		AgentScheduleID: opts.Case.ScheduleID,
		AgentConfig:     opts.AgentConfig,
		AgentBeforeResponse: func(sequence int) bool {
			if opts.RestartAfterCall == 0 || sequence != opts.RestartAfterCall {
				return false
			}
			boundary <- struct{}{}
			select {
			case <-resume:
			case <-runContext.Done():
			}
			return true
		},
	})
	if err != nil {
		return Result{}, err
	}
	if err := server.Start(); err != nil {
		return Result{}, err
	}
	defer func() {
		closeContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Close(closeContext)
	}()

	proxyURL := "http://" + server.StubAddress()
	adminURL := "http://" + server.AdminAddress()
	stateDir, err := os.MkdirTemp("", "infernosim-state-*")
	if err != nil {
		return Result{}, err
	}
	defer os.RemoveAll(stateDir)
	environment := mergeEnvironment(os.Environ(), opts.Environment, map[string]string{
		"HTTP_PROXY": proxyURL, "HTTPS_PROXY": proxyURL,
		"http_proxy": proxyURL, "https_proxy": proxyURL,
		"NO_PROXY": "", "no_proxy": "",
		"INFERNOSIM_PROXY_URL": proxyURL, "INFERNOSIM_ADMIN_URL": adminURL,
		"INFERNOSIM_CASE_ID": opts.Case.ID, "INFERNOSIM_FAULT_ID": opts.Case.FaultID,
		"INFERNOSIM_STATE_DIR": stateDir,
	})
	// Hooks receive the caller's normal network environment, not the replay proxy.
	hookEnvironment := mergeEnvironment(os.Environ(), opts.Environment, map[string]string{
		"INFERNOSIM_CASE_ID": opts.Case.ID, "INFERNOSIM_STATE_DIR": stateDir,
	})
	if len(opts.StateCheck.SetupCommand) > 0 {
		if _, err := runStateCommand(ctx, opts.StateCheck.SetupCommand, hookEnvironment, opts.StateCheck.timeout()); err != nil {
			return Result{}, fmt.Errorf("state setup failed (output withheld): %w", err)
		}
	}
	started := time.Now()
	process, runErr := executeWithRestart(runContext, opts, environment, boundary, resume)
	duration := time.Since(started)
	if runErr != nil {
		process.ExitCode = -1
		var exitError *exec.ExitError
		if errors.As(runErr, &exitError) {
			process.ExitCode = exitError.ExitCode()
		}
	}
	if errors.Is(runContext.Err(), context.DeadlineExceeded) {
		process.TimedOut = true
	}
	snapshot := server.Snapshot()
	result := Result{Version: 1, Case: opts.Case, Passed: true, Duration: duration, Process: process, Simulator: snapshot, ScopeHash: prepared.ScopeHash, RestartAfterCall: opts.RestartAfterCall}
	result.StateCheckHash = opts.StateCheck.Hash()
	if snapshot.Agent != nil {
		result.Assertions = agentreliability.Evaluate(prepared.Config.Agent, *snapshot.Agent, snapshot.Unexpected, duration)
	}
	var failures []string
	if len(opts.StateCheck.Command) > 0 {
		checks, checkErr := evaluateStateCheck(ctx, opts.StateCheck, hookEnvironment)
		result.Assertions = append(result.Assertions, checks...)
		if checkErr != nil {
			result.InvalidRun = true
			failures = append(failures, "state check invalid: "+checkErr.Error())
		}
	}
	if snapshot.Agent != nil && (snapshot.Agent.ScheduleFailed || snapshot.Agent.ScheduleCompleted != snapshot.Agent.ScheduleSteps) {
		result.InvalidRun = true
	}
	if opts.RestartAfterCall > 0 && !process.Restarted {
		result.InvalidRun = true
		failures = append(failures, "requested crash boundary was not exercised")
	}
	if process.TimedOut {
		failures = append(failures, "agent command exceeded timeout")
	} else if process.ExitCode != 0 {
		failures = append(failures, fmt.Sprintf("agent command exited with code %d", process.ExitCode))
	}
	for _, assertion := range result.Assertions {
		if !assertion.Passed {
			failures = append(failures, assertion.ID+": "+assertion.Message)
		}
	}
	if snapshot.Unexpected {
		result.InvalidRun = true
		failures = append(failures, "application called outside the recorded universe")
	}
	if len(snapshot.Divergences) > 0 {
		result.InvalidRun = true
		failures = append(failures, snapshot.Divergences...)
	}
	for _, id := range opts.Case.ActiveFaults() {
		if snapshot.Agent == nil || !contains(snapshot.Agent.AppliedFaults, id) {
			result.InvalidRun = true
			failures = append(failures, "selected fault was not triggered: "+id)
		}
	}
	if len(failures) > 0 {
		result.Passed = false
		result.Failure = strings.Join(failures, "; ")
	}
	return result, nil
}

func faultIDs(value agentreliability.Case) []string {
	return value.ActiveFaults()
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func hashScope(bundle replaydriver.IncidentBundle, configPath string) (string, error) {
	paths := []string{bundle.MetadataPath, bundle.InboundLog, bundle.OutboundLog, filepath.Join(bundle.Dir, "messages.log"), bundle.MCPLog, bundle.AgentSpans, configPath}
	sort.Strings(paths)
	hash := sha256.New()
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return "", err
		}
		_, _ = hash.Write([]byte(filepath.Base(path)))
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write(data)
		_, _ = hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func mergeEnvironment(base, extra []string, overrides map[string]string) []string {
	values := make(map[string]string)
	for _, entry := range append(append([]string(nil), base...), extra...) {
		key, value, found := strings.Cut(entry, "=")
		if found && key != "" {
			values[key] = value
		}
	}
	for key, value := range overrides {
		values[key] = value
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		result = append(result, key+"="+values[key])
	}
	return result
}

type boundedBuffer struct {
	mu        sync.Mutex
	buffer    bytes.Buffer
	limit     int
	truncated bool
}

func (b *boundedBuffer) Write(value []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	original := len(value)
	remaining := b.limit - b.buffer.Len()
	if remaining <= 0 {
		b.truncated = true
		return original, nil
	}
	if len(value) > remaining {
		value = value[:remaining]
		b.truncated = true
	}
	_, _ = b.buffer.Write(value)
	return original, nil
}

func (b *boundedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.String()
}

func WriteJSON(path string, value any) error {
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return reporting.WritePrivateFile(path, append(encoded, '\n'))
}

func ReportingResult(results []Result) reporting.Result {
	passed := 0
	var findings []reporting.Finding
	cases := make([]reporting.Case, 0, len(results))
	for _, result := range results {
		var messages []string
		if result.Failure != "" {
			messages = append(messages, result.Failure)
		}
		for _, assertion := range result.Assertions {
			messages = append(messages, assertion.Coverage+" "+assertion.ID+": "+assertion.Message)
		}
		if result.Passed {
			passed++
		} else {
			ruleID := "AGENT_CASE_FAILED"
			if result.Case.Category != "" {
				ruleID = "AGENT_" + strings.ToUpper(strings.ReplaceAll(result.Case.Category, "-", "_"))
			}
			level := "error"
			switch result.Case.Severity {
			case "low":
				level = "note"
			case "medium":
				level = "warning"
			}
			findings = append(findings, reporting.Finding{
				RuleID: ruleID, Level: level, Title: result.Case.Description,
				Message: result.Failure, Location: result.Case.ID,
			})
		}
		cases = append(cases, reporting.Case{
			ID: result.Case.ID, Name: result.Case.Description, Passed: result.Passed,
			Category: result.Case.Category, Severity: result.Case.Severity,
			Duration: result.Duration.Round(time.Millisecond).String(), Message: strings.Join(messages, "\n"),
		})
	}
	outcome := "PASS_AGENT_RELIABILITY"
	if passed != len(results) {
		outcome = "FAIL_AGENT_RELIABILITY"
	}
	return reporting.Result{
		Tool: "InfernoSIM", Category: "agent-reliability", Outcome: outcome,
		Summary:  fmt.Sprintf("%d/%d deterministic agent reliability cases passed", passed, len(results)),
		Findings: findings, Cases: cases,
	}
}

// ReliabilitySurface summarizes only observed configured cases. It is an
// unweighted pass rate, not a general model score or an extrapolation beyond
// the incident-derived matrix.
func ReliabilitySurface(results []Result) Surface {
	surface := Surface{Version: 1}
	byCategory := make(map[string]*SurfaceCategory)
	for _, result := range results {
		if result.Case.Baseline() {
			surface.BaselinePassed = result.Passed
			continue
		}
		surface.FaultCases++
		if result.Passed {
			surface.FaultsPassed++
		} else {
			surface.FaultsFailed++
		}
		category := result.Case.Category
		if category == "" {
			category = "custom"
		}
		item := byCategory[category]
		if item == nil {
			item = &SurfaceCategory{Category: category}
			byCategory[category] = item
		}
		item.Total++
		if result.Passed {
			item.Passed++
		} else {
			item.Failed++
		}
	}
	if surface.FaultCases > 0 {
		surface.ObservedPassRate = float64(surface.FaultsPassed) / float64(surface.FaultCases)
	}
	keys := make([]string, 0, len(byCategory))
	for category := range byCategory {
		keys = append(keys, category)
	}
	sort.Strings(keys)
	for _, category := range keys {
		item := byCategory[category]
		item.ObservedPassRate = float64(item.Passed) / float64(item.Total)
		surface.Categories = append(surface.Categories, *item)
	}
	return surface
}

var _ io.Writer = (*boundedBuffer)(nil)
