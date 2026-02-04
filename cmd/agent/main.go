package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
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
func runReplay(args []string) int {
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

	injectFlags := multiFlag{}
	fs.Var(
		&injectFlags,
		"inject",
		`Injection rule, e.g. --inject "dep=worldtimeapi.org latency=+200ms"`,
	)

	if err := fs.Parse(args); err != nil {
		summary := buildSummary(replaySummaryInput{
			RunsAttempted: *runs,
			Outcomes: []ReplayOutcome{
				{
					RunIndex:       1,
					TotalEvents:    0,
					Completed:      false,
					DivergenceType: DivergenceInvalidConfig,
					Detail:         fmt.Sprintf("Flag parse error: %v", err),
				},
			},
		})
		printReplaySummary(summary)
		return summary.ExitStatus
	}

	// ---- resolve logs ----
	inboundLog := filepath.Join(*incidentDir, "inbound.log")
	outboundLog := filepath.Join(*incidentDir, "outbound.log")

	summary := executeReplay(replayExecutionInput{
		Runs:        *runs,
		TimeScale:   *timeScale,
		Density:     *density,
		MinGap:      *minGap,
		MaxWallTime: *maxWallTime,
		MaxIdleTime: *maxIdleTime,
		MaxEvents:   *maxEvents,
		IncidentDir: *incidentDir,
		InboundLog:  inboundLog,
		OutboundLog: outboundLog,
		InjectFlags: injectFlags,
		TargetBase:  "http://localhost:18080",
	})
	printReplaySummary(summary)
	return summary.ExitStatus
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

type ReplayDivergence string

const (
	DivergenceDeterministic    ReplayDivergence = "DETERMINISTIC"
	DivergenceNonDeterministic ReplayDivergence = "NON_DETERMINISTIC"
	DivergenceTimeExpanded     ReplayDivergence = "TIME_EXPANDED"
	DivergenceStalled          ReplayDivergence = "STALLED"
	DivergenceInvalidConfig    ReplayDivergence = "INVALID_CONFIG"
	DivergenceInfraError       ReplayDivergence = "INFRA_ERROR"
)

type ReplayOutcome struct {
	RunIndex        int
	TotalEvents     int
	CompletedEvents int
	WallTime        time.Duration
	Completed       bool
	Fingerprint     string
	DivergenceType  ReplayDivergence
	Detail          string
}

type replayExecutionInput struct {
	Runs        int
	TimeScale   float64
	Density     float64
	MinGap      time.Duration
	MaxWallTime time.Duration
	MaxIdleTime time.Duration
	MaxEvents   int
	IncidentDir string
	InboundLog  string
	OutboundLog string
	InjectFlags []string
	TargetBase  string
	SkipStub    bool
}

type replaySummaryInput struct {
	RunsAttempted int
	Outcomes      []ReplayOutcome
}

type ReplaySummary struct {
	Lines      []string
	Outcomes   []ReplayOutcome
	ExitStatus int
	Outcome    ReplayDivergence
}

func executeReplay(input replayExecutionInput) ReplaySummary {
	start := time.Now()
	outcomes := []ReplayOutcome{}

	if _, err := os.Stat(input.InboundLog); err != nil {
		outcomes = append(outcomes, ReplayOutcome{
			RunIndex:       1,
			Completed:      false,
			DivergenceType: DivergenceInfraError,
			Detail:         fmt.Sprintf("Inbound log not found: %s (%v)", input.InboundLog, err),
		})
		return buildSummary(replaySummaryInput{RunsAttempted: input.Runs, Outcomes: outcomes})
	}
	if _, err := os.Stat(input.OutboundLog); err != nil {
		outcomes = append(outcomes, ReplayOutcome{
			RunIndex:       1,
			Completed:      false,
			DivergenceType: DivergenceInfraError,
			Detail:         fmt.Sprintf("Outbound log not found: %s (%v)", input.OutboundLog, err),
		})
		return buildSummary(replaySummaryInput{RunsAttempted: input.Runs, Outcomes: outcomes})
	}

	events, err := replaydriver.LoadInboundEvents(input.InboundLog)
	if err != nil {
		outcomes = append(outcomes, ReplayOutcome{
			RunIndex:       1,
			Completed:      false,
			DivergenceType: DivergenceInfraError,
			Detail:         fmt.Sprintf("Failed to load inbound log: %v", err),
		})
		return buildSummary(replaySummaryInput{RunsAttempted: input.Runs, Outcomes: outcomes})
	}
	if len(events) == 0 {
		outcomes = append(outcomes, ReplayOutcome{
			RunIndex:       1,
			Completed:      false,
			DivergenceType: DivergenceInfraError,
			Detail:         "No inbound requests found in incident",
		})
		return buildSummary(replaySummaryInput{RunsAttempted: input.Runs, Outcomes: outcomes})
	}
	if input.MaxEvents > 0 && len(events) > input.MaxEvents {
		events = events[:input.MaxEvents]
	}

	rules, err := inject.ParseRules(input.InjectFlags)
	if err != nil {
		detail := err.Error()
		if v, ok := err.(*inject.ValidationError); ok {
			sort.Strings(v.SupportedKeys)
			sort.Strings(v.UnsupportedKeys)
			detail = fmt.Sprintf("%s | supported=%s | unsupported=%s", v.Reason, strings.Join(v.SupportedKeys, ","), strings.Join(v.UnsupportedKeys, ","))
		}
		outcomes = append(outcomes, ReplayOutcome{
			RunIndex:       1,
			TotalEvents:    len(events),
			Completed:      false,
			DivergenceType: DivergenceInvalidConfig,
			Detail:         detail,
		})
		return buildSummary(replaySummaryInput{RunsAttempted: input.Runs, Outcomes: outcomes})
	}

	var stub *stubproxy.StubProxy
	if !input.SkipStub {
		var err error
		stub, err = stubproxy.New(input.OutboundLog, rules)
		if err != nil {
			outcomes = append(outcomes, ReplayOutcome{
				RunIndex:       1,
				Completed:      false,
				DivergenceType: DivergenceInfraError,
				Detail:         fmt.Sprintf("Stub proxy init failed: %v", err),
			})
			return buildSummary(replaySummaryInput{RunsAttempted: input.Runs, Outcomes: outcomes})
		}

		stubServer := &http.Server{
			Addr:    ":19000",
			Handler: stub,
		}

		go func() {
			log.Printf("Stub proxy active on :19000")
			if err := stubServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				log.Printf("Stub proxy error: %v", err)
			}
		}()
		time.Sleep(300 * time.Millisecond)
		defer func() {
			_ = stubServer.Close()
		}()
	}

	var referenceFingerprint [32]byte
	var referenceSet bool
	var referenceSignatures []string

	for i := 0; i < input.Runs; i++ {
		if stub != nil {
			stub.Reset()
		}
		runStart := time.Now()
		remaining := input.MaxWallTime - time.Since(start)
		if remaining <= 0 {
			outcomes = append(outcomes, ReplayOutcome{
				RunIndex:        i + 1,
				TotalEvents:     len(events),
				CompletedEvents: 0,
				WallTime:        time.Since(runStart),
				Completed:       false,
				DivergenceType:  DivergenceTimeExpanded,
				Detail:          "Replay exceeded max wall time before run start",
			})
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
		if err != nil {
			outcomes = append(outcomes, ReplayOutcome{
				RunIndex:       i + 1,
				TotalEvents:    len(events),
				Completed:      false,
				WallTime:       time.Since(runStart),
				DivergenceType: DivergenceInfraError,
				Detail:         fmt.Sprintf("Replay failed: %v", err),
			})
			break
		}

		if r.TimeExpanded {
			outcomes = append(outcomes, ReplayOutcome{
				RunIndex:        i + 1,
				TotalEvents:     r.TotalEvents,
				CompletedEvents: r.CompletedEvents,
				WallTime:        r.RunDuration,
				Completed:       false,
				DivergenceType:  DivergenceTimeExpanded,
				Detail:          r.TimeExpandedReason,
			})
			break
		}

		if r.Stalled {
			outcomes = append(outcomes, ReplayOutcome{
				RunIndex:        i + 1,
				TotalEvents:     r.TotalEvents,
				CompletedEvents: r.CompletedEvents,
				WallTime:        r.RunDuration,
				Completed:       false,
				DivergenceType:  DivergenceStalled,
				Detail:          r.StalledReason,
			})
			break
		}

		outcome := ReplayOutcome{
			RunIndex:        i + 1,
			TotalEvents:     r.TotalEvents,
			CompletedEvents: r.CompletedEvents,
			WallTime:        r.RunDuration,
			Completed:       true,
			Fingerprint:     fmt.Sprintf("%x", r.Fingerprint),
			DivergenceType:  DivergenceDeterministic,
		}

		if !referenceSet {
			referenceFingerprint = r.Fingerprint
			referenceSignatures = r.ResponseSignatures
			referenceSet = true
		} else if r.Fingerprint != referenceFingerprint {
			outcome.DivergenceType = DivergenceNonDeterministic
			_, dtype := firstSignatureDivergence(referenceSignatures, r.ResponseSignatures)
			if dtype != "" {
				outcome.Detail = mapNonDeterminismReason(dtype)
			}
		}

		if outcome.DivergenceType == DivergenceDeterministic && stub != nil {
			if stub.UnexpectedOutbound() || len(stub.DivergenceReasons()) > 0 {
				outcome.DivergenceType = DivergenceNonDeterministic
				outcome.Detail = "concurrent overlap"
			}
		}

		if outcome.DivergenceType == DivergenceNonDeterministic && outcome.Detail == "" {
			outcome.Detail = "timing sensitivity"
		}

		outcomes = append(outcomes, outcome)
	}

	return buildSummary(replaySummaryInput{RunsAttempted: input.Runs, Outcomes: outcomes})
}

func buildSummary(input replaySummaryInput) ReplaySummary {
	outcomes := input.Outcomes
	if len(outcomes) == 0 {
		outcomes = append(outcomes, ReplayOutcome{
			RunIndex:       1,
			Completed:      false,
			DivergenceType: DivergenceInfraError,
			Detail:         "Replay produced no outcomes",
		})
	}

	executed := len(outcomes)
	completed := 0
	deterministic := 0
	nonDeterministic := 0
	failureCounts := map[ReplayDivergence]int{}
	nonDetReasons := map[string]int{}
	primaryFailure := ""
	overallOutcome := DivergenceDeterministic
	for _, o := range outcomes {
		if o.Completed {
			completed++
		}
		switch o.DivergenceType {
		case DivergenceDeterministic:
			deterministic++
		case DivergenceNonDeterministic:
			nonDeterministic++
			failureCounts[o.DivergenceType]++
			if o.Detail != "" {
				nonDetReasons[o.Detail]++
			}
		default:
			failureCounts[o.DivergenceType]++
		}
		if primaryFailure == "" && o.DivergenceType != DivergenceDeterministic {
			primaryFailure = o.Detail
		}
	}
	if failureCounts[DivergenceInvalidConfig] > 0 {
		overallOutcome = DivergenceInvalidConfig
	} else if failureCounts[DivergenceInfraError] > 0 {
		overallOutcome = DivergenceInvalidConfig
	} else if failureCounts[DivergenceStalled] > 0 {
		overallOutcome = DivergenceStalled
	} else if failureCounts[DivergenceTimeExpanded] > 0 {
		overallOutcome = DivergenceTimeExpanded
	} else if nonDeterministic > 0 {
		overallOutcome = DivergenceNonDeterministic
	}

	stability := 0
	if executed > 0 {
		stability = int(float64(deterministic) / float64(executed) * 100.0)
	}

	lines := []string{
		"--------------------------------",
		"InfernoSIM Replay Summary",
		"--------------------------------",
		fmt.Sprintf("Outcome: %s", summaryOutcomeLabel(overallOutcome)),
		fmt.Sprintf("Runs attempted: %d", input.RunsAttempted),
		fmt.Sprintf("Runs executed: %d", executed),
		fmt.Sprintf("Runs completed: %d", completed),
		fmt.Sprintf("Deterministic runs: %d / %d", deterministic, executed),
		fmt.Sprintf("Non-deterministic runs: %d", nonDeterministic),
		fmt.Sprintf("Primary failure reason: %s", primaryFailureOrNone(primaryFailure)),
		fmt.Sprintf("Actionable recommendation: %s", recommendationForOutcome(overallOutcome)),
		"",
		"Failure modes observed:",
	}

	if len(failureCounts) == 0 {
		lines = append(lines, "- None")
	} else {
		for _, entry := range failureModeLines(failureCounts, nonDetReasons) {
			lines = append(lines, entry)
		}
	}

	lines = append(lines,
		"",
		fmt.Sprintf("Stability score: %d / 100", stability),
		"",
		"Interpretation:",
	)

	if deterministic == executed && executed > 0 {
		lines = append(lines, "- OK Application works correctly")
	} else {
		lines = append(lines, "- OK Application works correctly")
		lines = append(lines, "- WARN Behavior varies under identical traffic")
	}
	if failureCounts[DivergenceTimeExpanded] > 0 || failureCounts[DivergenceStalled] > 0 {
		lines = append(lines, "- WARN Replay limits reached before completion")
	}
	if failureCounts[DivergenceInvalidConfig] > 0 || failureCounts[DivergenceInfraError] > 0 {
		lines = append(lines, "- WARN Replay encountered configuration or infrastructure errors")
	}

	lines = append(lines,
		"",
		"Supported replay flags and modes:",
		"- --runs (determinism check)",
		"- --time-scale (forensic)",
		"- --density (stress)",
		"- --min-gap (safety)",
		"- --inject latency/timeout (fault injection)",
		"- --max-wall-time (safety)",
		"- --max-idle-time (safety)",
		"- --max-events (safety)",
		"",
		"Recommendations:",
		"- Use replay for forensic debugging",
		"- Use density mode for traffic tolerance",
		"- Add request-level timeouts for dependencies",
		"--------------------------------",
	)

	exitStatus := exitCodeFromOutcomes(outcomes)
	return ReplaySummary{
		Lines:      lines,
		Outcomes:   outcomes,
		ExitStatus: exitStatus,
		Outcome:    overallOutcome,
	}
}

func printReplaySummary(summary ReplaySummary) {
	out := strings.Join(summary.Lines, "\n") + "\n"
	fmt.Print(out)
	_ = os.WriteFile("replay_result.txt", []byte(out), 0644)
}

func firstSignatureDivergence(ref []string, candidate []string) (int, string) {
	min := len(ref)
	if len(candidate) < min {
		min = len(candidate)
	}
	for i := 0; i < min; i++ {
		if ref[i] != candidate[i] {
			return i, "response-based"
		}
	}
	if len(ref) != len(candidate) {
		return min, "ordering-based"
	}
	return -1, ""
}

func failureModeLines(counts map[ReplayDivergence]int, nonDetReasons map[string]int) []string {
	type entry struct {
		Label string
		Count int
	}
	entries := []entry{}
	for reason, count := range nonDetReasons {
		if count == 0 {
			continue
		}
		entries = append(entries, entry{
			Label: reason,
			Count: count,
		})
	}
	for mode, count := range counts {
		if count == 0 {
			continue
		}
		entries = append(entries, entry{
			Label: failureModeLabel(mode),
			Count: count,
		})
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Label < entries[j].Label
	})
	lines := make([]string, 0, len(entries))
	for _, e := range entries {
		lines = append(lines, fmt.Sprintf("- %s (%d)", e.Label, e.Count))
	}
	return lines
}

func failureModeLabel(mode ReplayDivergence) string {
	switch mode {
	case DivergenceNonDeterministic:
		return "Response variance"
	case DivergenceTimeExpanded:
		return "Timing sensitivity"
	case DivergenceStalled:
		return "Stalled replay"
	case DivergenceInvalidConfig:
		return "Invalid config"
	case DivergenceInfraError:
		return "Infrastructure error"
	default:
		return string(mode)
	}
}

func summaryOutcomeLabel(mode ReplayDivergence) string {
	switch mode {
	case DivergenceDeterministic:
		return "PASS"
	case DivergenceNonDeterministic:
		return "NON_DETERMINISTIC"
	case DivergenceStalled:
		return "STALLED"
	case DivergenceTimeExpanded:
		return "TIME_EXPANDED"
	case DivergenceInvalidConfig:
		return "INVALID_CONFIG"
	case DivergenceInfraError:
		return "INVALID_CONFIG"
	default:
		return string(mode)
	}
}

func primaryFailureOrNone(reason string) string {
	if reason == "" {
		return "none"
	}
	return reason
}

func recommendationForOutcome(mode ReplayDivergence) string {
	switch mode {
	case DivergenceDeterministic:
		return "Keep using replay for regression detection."
	case DivergenceNonDeterministic:
		return "Use replay for forensic analysis only and disable retries for deterministic replay."
	case DivergenceTimeExpanded:
		return "Reduce --runs, increase --density, or reduce --time-scale."
	case DivergenceStalled:
		return "Verify target service availability or increase --max-idle-time."
	case DivergenceInvalidConfig:
		return "Use only supported flags and inject keys."
	default:
		return "Inspect replay logs for infrastructure issues."
	}
}

func mapNonDeterminismReason(reason string) string {
	switch reason {
	case "ordering-based":
		return "response ordering variance"
	case "response-based":
		return "dependency variance"
	default:
		return "concurrent overlap"
	}
}

func exitCodeFromOutcomes(outcomes []ReplayOutcome) int {
	nonDeterministic := false
	for _, o := range outcomes {
		if o.DivergenceType == DivergenceInfraError || o.DivergenceType == DivergenceInvalidConfig {
			return 2
		}
		if o.DivergenceType != DivergenceDeterministic {
			nonDeterministic = true
		}
	}
	if nonDeterministic {
		return 1
	}
	return 0
}
