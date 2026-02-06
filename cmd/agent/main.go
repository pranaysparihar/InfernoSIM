package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"infernosim/pkg/capture"
	"infernosim/pkg/event"
	"infernosim/pkg/inject"
	"infernosim/pkg/replaydriver"
	"infernosim/pkg/stubproxy"
)

/*
ENTRYPOINT
*/
func main() {
	// ---- SUBCOMMAND: replay ----
	if len(os.Args) > 1 && os.Args[1] == "replay" {
		code := runReplay(os.Args[2:])
		os.Exit(code)
	}

	// ---- DEFAULT: capture agent ----
	runAgent()
}

/*
PHASE 1 / 2: CAPTURE AGENT
*/
func runAgent() {
	mode := flag.String("mode", "inbound", "Mode: 'inbound' or 'proxy'")
	listen := flag.String("listen", ":8080", "Listen address")
	forward := flag.String("forward", "", "Forward address (inbound mode)")
	logFile := flag.String("log", "events.log", "Event log file")
	flag.Parse()

	logger, err := event.NewLogger(*logFile)
	if err != nil {
		log.Fatalf("Failed to open log file: %v", err)
	}
	defer logger.Close()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	log.Printf("InfernoSIM agent starting | mode=%s listen=%s", *mode, *listen)

	switch *mode {

	case "inbound":
		if *forward == "" {
			log.Fatal("Inbound mode requires --forward host:port")
		}

		targetURL := &url.URL{Scheme: "http", Host: *forward}
		server, err := capture.StartInboundProxy(*listen, targetURL, logger)
		if err != nil {
			log.Fatalf("Failed to start inbound proxy: %v", err)
		}

		log.Printf("Inbound proxy active → %s", *forward)
		<-stop
		log.Println("Shutting down inbound proxy")
		_ = server.Close()

	case "proxy":
		server, err := capture.StartForwardProxy(*listen, logger)
		if err != nil {
			log.Fatalf("Failed to start forward proxy: %v", err)
		}

		log.Printf("Outbound proxy active")
		<-stop
		log.Println("Shutting down outbound proxy")
		_ = server.Close()

	default:
		log.Fatalf("Unknown mode: %s", *mode)
	}

	log.Println("InfernoSIM agent stopped")
}

/*
PHASE 3: REPLAY + SIMULATION
*/
func runReplay(args []string) (code int) {
	summary := NewReplaySummary()
	defer func() {
		if r := recover(); r != nil {
			summary.PrimaryFailureReason = fmt.Sprintf("panic: %v", r)
			summary.Outcome = "FAIL_INVALID_ENV"
		}
		summary.Finalize()
		summary.Print()
		code = summary.ExitStatus
	}()

	fs := flag.NewFlagSet("replay", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	incidentDir := fs.String(
		"incident",
		".",
		"Incident directory (contains inbound.log and outbound.log)",
	)

	timeScale := fs.Float64(
		"time-scale",
		1.0,
		"Time scale (forensic replay): 0.1=10x faster, 2.0=2x slower",
	)

	density := fs.Float64(
		"density",
		1.0,
		"Replay density multiplier (CI/stress): 1=faithful, 10=10x denser",
	)

	minGap := fs.Duration(
		"min-gap",
		2*time.Millisecond,
		"Minimum gap between replayed requests (prevents busy loops)",
	)

	runs := fs.Int(
		"runs",
		10,
		"Number of replay runs",
	)

	maxWallTime := fs.Duration(
		"max-wall-time",
		30*time.Second,
		"Maximum wall-clock time for replay",
	)

	maxIdleTime := fs.Duration(
		"max-idle-time",
		5*time.Second,
		"Maximum idle time without replay progress",
	)

	maxEvents := fs.Int(
		"max-events",
		0,
		"Maximum number of inbound events to replay (0 = no cap)",
	)
	targetBase := fs.String(
		"target-base",
		"http://localhost:18080",
		"Replay target base URL for inbound request playback",
	)
	stubListen := fs.String(
		"stub-listen",
		":19000",
		"Replay stub proxy listen address",
	)
	stubCompatListen := fs.String(
		"stub-compat-listen",
		":9000",
		"Optional compatibility listen address for apps using a fixed outbound proxy port",
	)

	injectFlags := multiFlag{}
	fs.Var(
		&injectFlags,
		"inject",
		`Injection rule, e.g. --inject "dep=worldtimeapi.org latency=+200ms"`,
	)

	if err := fs.Parse(args); err != nil {
		summary.PrimaryFailureReason = fmt.Sprintf("Flag parse error: %v", err)
		summary.Outcome = "FAIL_INVALID_ENV"
		return
	}

	// ---- resolve logs ----
	inboundLog := filepath.Join(*incidentDir, "inbound.log")
	outboundLog := filepath.Join(*incidentDir, "outbound.log")

	executeReplay(replayExecutionInput{
		Runs:        *runs,
		TimeScale:   *timeScale,
		Density:     *density,
		MinGap:      *minGap,
		MaxWallTime: *maxWallTime,
		MaxIdleTime: *maxIdleTime,
		MaxEvents:   *maxEvents,
		InboundLog:  inboundLog,
		OutboundLog: outboundLog,
		InjectFlags: injectFlags,
		TargetBase:  *targetBase,
		StubListen:  *stubListen,
		StubCompat:  *stubCompatListen,
	}, &summary)
	return
}

/*
HELPER: multi-value --inject flag
*/
type multiFlag []string

func (m *multiFlag) String() string { return "" }
func (m *multiFlag) Set(v string) error {
	*m = append(*m, v)
	return nil
}

type ReplayOutcome struct {
	RunIndex        int
	TotalEvents     int
	CompletedEvents int
	WallTime        time.Duration
	Completed       bool
	Fingerprint     string
	Detail          string
}

type ReplaySummary struct {
	Outcome                string
	RunsRequested          int
	RunsExecuted           int
	RunsCompleted          int
	InboundEventsReplayed  int
	OutboundEventsObserved int
	OutboundEventsExpected int
	ProxyStatus            string
	InjectionsApplied      string
	DependenciesExercised  bool
	DeterministicRuns      int
	NonDeterministicRuns   int
	PrimaryFailureReason   string
	Recommendation         string
	WhatNotTested          []string
	Outcomes               []ReplayOutcome
	Lines                  []string
	ExitStatus             int
	TransparentMode        bool
}

type replayExecutionInput struct {
	Runs        int
	TimeScale   float64
	Density     float64
	MinGap      time.Duration
	MaxWallTime time.Duration
	MaxIdleTime time.Duration
	MaxEvents   int
	InboundLog  string
	OutboundLog string
	InjectFlags []string
	TargetBase  string
	StubListen  string
	StubCompat  string
}

func NewReplaySummary() ReplaySummary {
	return ReplaySummary{
		ProxyStatus:           "UNKNOWN",
		InjectionsApplied:     "none",
		DependenciesExercised: false,
	}
}

func (s *ReplaySummary) Finalize() {
	s.Outcome = computeOutcome(s)
	s.Recommendation = recommendationForOutcome(s.Outcome)
	s.WhatNotTested = computeWhatNotTested(s)
	s.ExitStatus = exitCodeFromOutcome(s.Outcome)
	s.Lines = buildSummaryLines(s)
}

func (s *ReplaySummary) Print() {
	out := strings.Join(s.Lines, "\n") + "\n"
	fmt.Print(out)
	_ = os.WriteFile("replay_result.txt", []byte(out), 0644)
}

func executeReplay(input replayExecutionInput, summary *ReplaySummary) {
	start := time.Now()
	summary.RunsRequested = input.Runs
	summary.TransparentMode = os.Getenv("INFERNOSIM_TRANSPARENT") == "1"

	if _, err := os.Stat(input.InboundLog); err != nil {
		summary.PrimaryFailureReason = fmt.Sprintf("Inbound log not found: %s (%v)", input.InboundLog, err)
		summary.Outcome = "FAIL_INVALID_ENV"
		return
	}
	if _, err := os.Stat(input.OutboundLog); err != nil {
		summary.PrimaryFailureReason = fmt.Sprintf("Outbound log not found: %s (%v)", input.OutboundLog, err)
		summary.Outcome = "FAIL_INVALID_ENV"
		return
	}

	events, err := replaydriver.LoadInboundEvents(input.InboundLog)
	if err != nil {
		summary.PrimaryFailureReason = fmt.Sprintf("Failed to load inbound log: %v", err)
		summary.Outcome = "FAIL_INVALID_ENV"
		return
	}
	if len(events) == 0 {
		summary.PrimaryFailureReason = "No inbound requests found in incident"
		summary.Outcome = "FAIL_NO_COVERAGE"
		return
	}
	if input.MaxEvents > 0 && len(events) > input.MaxEvents {
		events = events[:input.MaxEvents]
	}

	expectedOutbound, err := stubproxy.LoadOutboundEvents(input.OutboundLog)
	if err == nil {
		summary.OutboundEventsExpected = len(expectedOutbound)
	}

	rules, err := inject.ParseRules(input.InjectFlags)
	if err != nil {
		summary.PrimaryFailureReason = fmt.Sprintf("Invalid injection: %v", err)
		summary.Outcome = "FAIL_INVALID_ENV"
		return
	}
	summary.InjectionsApplied = injectionsAppliedLabel(rules)

	stub, err := stubproxy.New(input.OutboundLog, input.OutboundLog, rules)
	if err != nil {
		summary.PrimaryFailureReason = fmt.Sprintf("Stub proxy init failed: %v", err)
		summary.Outcome = "FAIL_INVALID_ENV"
		return
	}

	cleanupRules := func() {}
	if summary.TransparentMode {
		cleanup, err := installTransparentRedirect()
		if err != nil {
			summary.PrimaryFailureReason = fmt.Sprintf("iptables setup failed: %v", err)
			summary.Outcome = "FAIL_INVALID_ENV"
			return
		}
		cleanupRules = cleanup
	}
	defer cleanupRules()

	stubListen := input.StubListen
	if strings.TrimSpace(stubListen) == "" {
		stubListen = ":19000"
	}
	listener, err := net.Listen("tcp", stubListen)
	if err != nil {
		summary.ProxyStatus = "FAILED"
		summary.PrimaryFailureReason = fmt.Sprintf("Stub proxy bind failed: %v", err)
		summary.Outcome = "FAIL_INVALID_ENV"
		return
	}
	summary.ProxyStatus = "BOUND"

	go func() {
		log.Printf("Stub proxy active on %s", stubListen)
		if summary.TransparentMode {
			if err := stub.ServeTransparent(listener); err != nil && !errors.Is(err, net.ErrClosed) {
				log.Printf("Stub proxy error: %v", err)
			}
			return
		}
		stubServer := &http.Server{Handler: stub}
		if err := stubServer.Serve(listener); err != nil && err != http.ErrServerClosed {
			log.Printf("Stub proxy error: %v", err)
		}
	}()
	defer func() {
		_ = listener.Close()
	}()
	if !summary.TransparentMode {
		compatListen := strings.TrimSpace(input.StubCompat)
		if compatListen != "" && compatListen != stubListen {
			compatListener, compatErr := net.Listen("tcp", compatListen)
			if compatErr != nil {
				log.Printf("Stub proxy compat listen skipped on %s: %v", compatListen, compatErr)
			} else {
				go func() {
					log.Printf("Stub proxy compat active on %s", compatListen)
					stubServer := &http.Server{Handler: stub}
					if err := stubServer.Serve(compatListener); err != nil && err != http.ErrServerClosed {
						log.Printf("Stub proxy compat error: %v", err)
					}
				}()
				defer func() {
					_ = compatListener.Close()
				}()
			}
		}
	}

	var referenceFingerprint [32]byte
	var referenceSet bool
	var nonDeterministic bool

	for i := 0; i < input.Runs; i++ {
		stub.Reset()
		runStart := time.Now()
		remaining := input.MaxWallTime - time.Since(start)
		if remaining <= 0 {
			summary.PrimaryFailureReason = "Replay exceeded max wall time before run start"
			break
		}

		r, err := replaydriver.ReplayEvents(
			events,
			input.TargetBase,
			replaydriver.ReplayConfig{
				TimeScale:    input.TimeScale,
				Density:      input.Density,
				MinGap:       input.MinGap,
				MaxWallClock: remaining,
				MaxIdleTime:  input.MaxIdleTime,
				MaxEvents:    input.MaxEvents,
			},
		)
		summary.RunsExecuted++
		if err != nil {
			summary.PrimaryFailureReason = fmt.Sprintf("Replay failed: %v", err)
			summary.Outcome = "FAIL_STALLED"
			break
		}

		if r.TimeExpanded || r.Stalled {
			summary.PrimaryFailureReason = r.TimeExpandedReason
			if r.Stalled {
				summary.PrimaryFailureReason = r.StalledReason
			}
			summary.Outcome = "FAIL_STALLED"
			break
		}

		summary.RunsCompleted++
		summary.InboundEventsReplayed += r.CompletedEvents
		outboundObserved := stub.ObservedCount()
		summary.OutboundEventsObserved += outboundObserved
		summary.DependenciesExercised = summary.OutboundEventsObserved > 0
		if stub.ForwardErrors() > 0 {
			summary.PrimaryFailureReason = "Proxy forwarding failed"
			summary.Outcome = "FAIL_PROXY_FORWARDING"
			break
		}

		outcome := ReplayOutcome{
			RunIndex:        i + 1,
			TotalEvents:     r.TotalEvents,
			CompletedEvents: r.CompletedEvents,
			WallTime:        r.RunDuration,
			Completed:       true,
			Fingerprint:     fmt.Sprintf("%x", r.Fingerprint),
		}
		summary.Outcomes = append(summary.Outcomes, outcome)

		if !referenceSet {
			referenceFingerprint = r.Fingerprint
			referenceSet = true
		} else if r.Fingerprint != referenceFingerprint {
			nonDeterministic = true
		}

		_ = runStart
	}

	if summary.RunsExecuted > 1 && nonDeterministic {
		summary.NonDeterministicRuns = 1
		if summary.PrimaryFailureReason == "" {
			summary.PrimaryFailureReason = "Non-deterministic fingerprints observed"
		}
	} else {
		summary.DeterministicRuns = summary.RunsCompleted
	}
}

func computeOutcome(summary *ReplaySummary) string {
	if strings.HasPrefix(summary.Outcome, "FAIL_") {
		return summary.Outcome
	}
	if summary.ProxyStatus == "FAILED" {
		return "FAIL_INVALID_ENV"
	}
	if summary.TransparentMode && summary.OutboundEventsExpected > 0 && summary.OutboundEventsObserved == 0 {
		return "FAIL_TRANSPARENT_PROXY"
	}
	if summary.OutboundEventsObserved == 0 {
		return "FAIL_NO_COVERAGE"
	}
	if summary.RunsExecuted > 1 && summary.NonDeterministicRuns > 0 {
		return "FAIL_NON_DETERMINISTIC"
	}
	if summary.PrimaryFailureReason != "" && summary.RunsCompleted == 0 {
		return "FAIL_STALLED"
	}
	if summary.RunsCompleted == summary.RunsRequested && summary.DependenciesExercised {
		return "PASS_STRONG"
	}
	return "PASS_WEAK"
}

func computeWhatNotTested(summary *ReplaySummary) []string {
	var gaps []string
	if summary.InboundEventsReplayed == 0 {
		gaps = append(gaps, "No inbound events replayed")
	}
	if summary.OutboundEventsObserved == 0 {
		gaps = append(gaps, "No outbound calls observed")
	}
	if summary.TransparentMode && summary.OutboundEventsExpected > 0 && summary.OutboundEventsObserved == 0 {
		gaps = append(gaps, "Transparent redirect did not capture outbound traffic")
	}
	if summary.ProxyStatus != "BOUND" {
		gaps = append(gaps, "Outbound stub proxy not bound")
	}
	if !summary.DependenciesExercised {
		gaps = append(gaps, "Dependencies not exercised")
	}
	if summary.InjectionsApplied == "none" {
		gaps = append(gaps, "Fault injections not exercised")
	}
	if summary.RunsExecuted < summary.RunsRequested {
		gaps = append(gaps, "Not all requested runs executed")
	}
	return gaps
}

func buildSummaryLines(summary *ReplaySummary) []string {
	lines := []string{
		"--------------------------------",
		"InfernoSIM Replay Summary",
		"--------------------------------",
		fmt.Sprintf("Outcome: %s", summary.Outcome),
		fmt.Sprintf("Runs requested: %d", summary.RunsRequested),
		fmt.Sprintf("Runs executed: %d", summary.RunsExecuted),
		fmt.Sprintf("Runs completed: %d", summary.RunsCompleted),
		fmt.Sprintf("Deterministic runs: %d / %d", summary.DeterministicRuns, summary.RunsExecuted),
		fmt.Sprintf("Inbound events replayed: %d", summary.InboundEventsReplayed),
		fmt.Sprintf("Outbound events observed: %d", summary.OutboundEventsObserved),
		fmt.Sprintf("Outbound events expected: %d", summary.OutboundEventsExpected),
		fmt.Sprintf("Stub proxy status: %s", summary.ProxyStatus),
		fmt.Sprintf("Injections applied: %s", summary.InjectionsApplied),
		fmt.Sprintf("Dependencies exercised: %t", summary.DependenciesExercised),
		fmt.Sprintf("Primary failure reason: %s", primaryFailureOrNone(summary.PrimaryFailureReason)),
		fmt.Sprintf("Actionable recommendation: %s", summary.Recommendation),
		"",
		"WHAT THIS RUN DID NOT TEST",
	}

	if len(summary.WhatNotTested) == 0 {
		lines = append(lines, "- None")
	} else {
		for _, item := range summary.WhatNotTested {
			lines = append(lines, fmt.Sprintf("- %s", item))
		}
	}

	lines = append(lines, "--------------------------------")
	return lines
}

func injectionsAppliedLabel(rules []inject.Rule) string {
	if len(rules) == 0 {
		return "none"
	}
	hasLatency := false
	hasTimeout := false
	for _, r := range rules {
		if r.AddLatency > 0 {
			hasLatency = true
		}
		if r.Timeout > 0 {
			hasTimeout = true
		}
	}
	if hasLatency && hasTimeout {
		return "latency+timeout"
	}
	if hasLatency {
		return "latency"
	}
	if hasTimeout {
		return "timeout"
	}
	return "none"
}

func recommendationForOutcome(outcome string) string {
	switch outcome {
	case "PASS_STRONG":
		return "Keep using replay for regression detection."
	case "PASS_WEAK":
		return "Increase coverage by exercising dependencies and completing all runs."
	case "FAIL_NON_DETERMINISTIC":
		return "Disable retries and reduce concurrency for deterministic replay."
	case "FAIL_INVALID_ENV":
		return "Fix environment permissions, ports, or configuration and retry."
	case "FAIL_PROXY_FORWARDING":
		return "Ensure HTTP_PROXY points to InfernoSIM and outbound forwarding is reachable."
	case "FAIL_TRANSPARENT_PROXY":
		return "Verify iptables redirect to port 19000 and ensure NET_ADMIN is enabled."
	case "FAIL_NO_COVERAGE":
		return "Ensure outbound dependencies are reachable and instrumented."
	case "FAIL_STALLED":
		return "Reduce load or increase timeouts to avoid stalls."
	default:
		return "Inspect logs for additional details."
	}
}

func primaryFailureOrNone(reason string) string {
	if reason == "" {
		return "none"
	}
	return reason
}

func exitCodeFromOutcome(outcome string) int {
	switch outcome {
	case "PASS_STRONG":
		return 0
	case "PASS_WEAK":
		return 1
	case "FAIL_NON_DETERMINISTIC":
		return 1
	default:
		return 2
	}
}

func installTransparentRedirect() (func(), error) {
	rules := [][]string{
		{"-t", "nat", "-A", "OUTPUT", "-p", "tcp", "--dport", "80", "-j", "REDIRECT", "--to-ports", "19000"},
		{"-t", "nat", "-A", "OUTPUT", "-p", "tcp", "--dport", "443", "-j", "REDIRECT", "--to-ports", "19000"},
	}
	for _, args := range rules {
		if err := execCommand("iptables", args...); err != nil {
			return func() {}, err
		}
	}
	return func() {
		for i := len(rules) - 1; i >= 0; i-- {
			_ = execCommand("iptables", append([]string{"-t", "nat", "-D", "OUTPUT"}, rules[i][4:]...)...)
		}
	}, nil
}

func execCommand(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	return cmd.Run()
}
