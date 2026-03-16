package main

import (
	"encoding/json"
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
	"sync"
	"syscall"
	"time"

	"infernosim/pkg/capture"
	"infernosim/pkg/event"
	"infernosim/pkg/inject"
	"infernosim/pkg/replay"
	"infernosim/pkg/replaydriver"
	"infernosim/pkg/stubproxy"
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}
	switch os.Args[1] {
	case "record":
		os.Exit(runRecord(os.Args[2:]))
	case "replay":
		os.Exit(runReplay(os.Args[2:]))
	case "inspect":
		os.Exit(runInspect(os.Args[2:]))
	case "verify":
		os.Exit(runVerify(os.Args[2:]))
	case "diff":
		os.Exit(runDiff(os.Args[2:]))
	// legacy commands kept for backwards compatibility
	case "search":
		runSearch(os.Args[2:])
		os.Exit(0)
	case "strict-replay":
		runStrictReplay(os.Args[2:])
		os.Exit(0)
	default:
		fmt.Fprintf(os.Stderr, "infernosim: unknown command %q\n\n", os.Args[1])
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Fprintln(os.Stderr, `Usage: infernosim <command> [flags]

Commands:
  record   Capture incident traffic (starts an inbound proxy)
  inspect  Analyse an incident bundle (dependency graph, timeline)
  verify   Check replay safety of an incident bundle
  replay   Replay a captured incident against a target
  diff     Replay and show divergences from the captured baseline`)
}

func runStrictReplay(args []string) {
	log.Println("Starting Strict Replay Context")
	replayCmd := flag.NewFlagSet("strict-replay", flag.ExitOnError)
	rLogFile := replayCmd.String("log", "events.log", "Event log to replay")
	rTarget := replayCmd.String("target", "", "Target base URL (e.g. http://localhost:8081)")
	replayCmd.Parse(args)

	if *rTarget == "" {
		log.Fatal("strict-replay requires --target")
	}

	replayer, err := replay.NewReplayer(*rLogFile, replay.ReplayConfig{Strict: true})
	if err != nil {
		log.Fatalf("Failed to initialize replay: %v", err)
	}

	if err := replayer.PreflightValidation(*rTarget); err != nil {
		log.Fatalf("PASS_FAIL: %v", err)
	}

	if err := replayer.Replay(); err != nil {
		log.Fatalf("Replay divergence: %v", err)
	}
}

func runSearch(args []string) {
	log.Println("Starting Search (Envelope) Subcommand Context")
	searchCmd := flag.NewFlagSet("search", flag.ExitOnError)
	sLogFile := searchCmd.String("log", "events.log", "Event log to replay")
	sTarget := searchCmd.String("target", "", "Target base URL")
	searchCmd.Parse(args)

	if *sTarget == "" {
		log.Fatal("Search requires --target")
	}

	fmt.Printf("Searching envelope for target %s using %s\n", *sTarget, *sLogFile)

	replayer, err := replay.NewReplayer(*sLogFile, replay.ReplayConfig{Strict: false})
	if err != nil {
		log.Fatalf("Failed to initialize replay search: %v", err)
	}

	if err := replayer.PreflightValidation(*sTarget); err != nil {
		log.Fatalf("PASS_FAIL: %v", err)
	}

	fanout := 1
	for {
		fmt.Printf("Testing fanout multiplier %d...\n", fanout)
		err := replayer.Replay()
		if err != nil {
			fmt.Printf("=== FAIL_SLO_MISSED: Envelope breached at fanout %d ===\n", fanout)
			break
		}
		if fanout >= 5 {
			fmt.Printf("=== PASS_STRONG: Envelope stable up to fanout %d ===\n", fanout)
			break
		}
		fanout++
	}
}

func runAgent() {
	mode := flag.String("mode", "inbound", "Mode: 'inbound' or 'proxy'")
	listen := flag.String("listen", ":8080", "Listen address")
	forward := flag.String("forward", "", "Forward address (inbound mode)")
	logFile := flag.String("log", "events.log", "Event log file")
	httpsMode := flag.String("https-mode", "tunnel", "Outbound HTTPS behavior: 'tunnel' or 'mitm'")
	injectParam := flag.String("inject", "", "Fault injection config (e.g. jitter=50ms,drop=5%,reset=5%,status=503,rate=10%)")
	flag.Parse()

	logger, err := event.NewLogger(*logFile)
	if err != nil {
		log.Fatalf("Failed to open log file: %v", err)
	}
	defer logger.Close()

	injectCfg, err := inject.ParseConfig(*injectParam, 0)
	if err != nil {
		log.Fatalf("Failed to parse injection config: %v", err)
	}

	useMITM := (*httpsMode == "mitm")
	var caStore *capture.CAStore
	if useMITM {
		caStore, err = capture.NewCAStore()
		if err != nil {
			log.Fatalf("Failed to initialize CA store: %v", err)
		}
		log.Println("HTTPS MITM Inspection ENABLED")
	} else {
		log.Println("HTTPS Tunneling only (No MITM inspection)")
	}

	ctx := &capture.ProxyContext{
		Logger:  logger,
		CA:      caStore,
		Inject:  injectCfg,
		UseMITM: useMITM,
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	log.Printf("InfernoSIM agent starting | mode=%s listen=%s", *mode, *listen)

	switch *mode {
	case "inbound":
		if *forward == "" {
			log.Fatal("Inbound mode requires --forward host:port")
		}
		targetURL := &url.URL{Scheme: "http", Host: *forward}
		server, err := capture.StartInboundProxy(*listen, targetURL, ctx)
		if err != nil {
			log.Fatalf("Failed to start inbound proxy: %v", err)
		}
		log.Printf("Inbound proxy active → %s", *forward)
		<-stop
		log.Println("Shutting down inbound proxy")
		_ = server.Close()

	case "proxy":
		server, err := capture.StartForwardProxy(*listen, ctx)
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
func runReplay(args []string) (code int) {
	summary := NewReplaySummary()
	summary.PreviousRun = loadReplaySnapshot()
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
	fanout := fs.Int(
		"fanout",
		1,
		"Concurrent causal replay workers per run",
	)
	window := fs.Duration(
		"window",
		0,
		"SLO evaluation window; when set, replay fails if target throughput is not achieved within this duration",
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
	diff := fs.Bool("diff", false, "Show detailed differences between captured and replayed events")
	safeMode := fs.Bool("safe-mode", false, "Skip non-idempotent requests (POST/PUT/PATCH/DELETE) during replay")
	configFile := fs.String("config", "", "Path to replay.yaml config file (overrides defaults)")

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

	// ---- apply replay.yaml config (flags override yaml) ----
	resolvedConfigFile := *configFile
	if resolvedConfigFile == "" {
		bundleConfig := filepath.Join(*incidentDir, "replay.yaml")
		if _, err := os.Stat(bundleConfig); err == nil {
			resolvedConfigFile = bundleConfig
		}
	}
	if resolvedConfigFile != "" {
		if yamlCfg, err := replaydriver.LoadReplayConfig(resolvedConfigFile); err == nil {
			if yamlCfg.Target != "" && *targetBase == "http://localhost:18080" {
				*targetBase = yamlCfg.Target
			}
			if yamlCfg.Runs > 0 && *runs == 10 {
				*runs = yamlCfg.Runs
			}
			if yamlCfg.TimeScale > 0 && *timeScale == 1.0 {
				*timeScale = yamlCfg.TimeScale
			}
			if yamlCfg.SafeMode {
				*safeMode = true
			}
		}
	}

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
		Fanout:      *fanout,
		Window:      *window,
		Diff:        *diff,
		SafeMode:    *safeMode,
		ConfigFile:  resolvedConfigFile,
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
	OutboundVerification   string
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
	Fanout                 int
	Window                 time.Duration
	TargetInbound          int
	TargetOutbound         int
	Elapsed                time.Duration
	AchievedRPS            float64
	TargetRPS              float64
	LimitingFactor         string
	EnvelopeInboundRPS     string
	EnvelopeFanout         string
	EnvelopeLatency        string
	DeltaFanout            string
	DeltaRate              string
	DeltaOutbound          string
	MaxInjectedLatency     time.Duration
	MaxInjectedTimeout     time.Duration
	PreviousRun            *ReplaySnapshot
	Diff                   bool
	DiffResults            []*replaydriver.EventDiff
}

type ReplaySnapshot struct {
	Timestamp        time.Time `json:"timestamp"`
	Outcome          string    `json:"outcome"`
	Fanout           int       `json:"fanout"`
	AchievedRPS      float64   `json:"achieved_rps"`
	OutboundObserved int       `json:"outbound_observed"`
	OutboundTarget   int       `json:"outbound_target"`
	MaxLatencyMS     int64     `json:"max_latency_ms"`
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
	Fanout      int
	Window      time.Duration
	Diff        bool
	SafeMode    bool
	ConfigFile  string
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
	s.LimitingFactor = deriveLimitingFactor(s)
	deriveEnvelope(s)
	deriveDelta(s)
	s.Recommendation = recommendationForOutcome(s.Outcome)
	s.WhatNotTested = computeWhatNotTested(s)
	s.ExitStatus = exitCodeFromOutcome(s.Outcome)
	s.Lines = buildSummaryLines(s)
}

func (s *ReplaySummary) Print() {
	out := strings.Join(s.Lines, "\n") + "\n"
	fmt.Print(out)
	_ = os.WriteFile("replay_result.txt", []byte(out), 0644)
	saveReplaySnapshot(s)
}

func executeReplay(input replayExecutionInput, summary *ReplaySummary) {
	start := time.Now()
	summary.RunsRequested = input.Runs
	summary.TransparentMode = os.Getenv("INFERNOSIM_TRANSPARENT") == "1"
	if input.Fanout < 1 {
		summary.PrimaryFailureReason = "fanout must be >= 1"
		summary.Outcome = "FAIL_INVALID_ENV"
		return
	}
	summary.Fanout = input.Fanout
	summary.Window = input.Window

	if _, err := os.Stat(input.InboundLog); err != nil {
		summary.PrimaryFailureReason = fmt.Sprintf("Inbound log not found: %s (%v)", input.InboundLog, err)
		summary.Outcome = "FAIL_INVALID_ENV"
		return
	}
	if _, err := os.Stat(input.OutboundLog); err == nil {
		summary.OutboundVerification = "enabled"
	} else {
		summary.OutboundVerification = "skipped (no outbound.log)"
		fmt.Println("note: outbound.log not found — running inbound-only replay")
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
	expectedOutboundPerReplay := 0
	if err == nil {
		expectedOutboundPerReplay = len(expectedOutbound)
	}
	summary.TargetInbound = len(events) * input.Runs * input.Fanout
	summary.TargetOutbound = expectedOutboundPerReplay * input.Runs * input.Fanout
	summary.OutboundEventsExpected = summary.TargetOutbound

	rules, err := inject.ParseRules(input.InjectFlags)
	if err != nil {
		summary.PrimaryFailureReason = fmt.Sprintf("Invalid injection: %v", err)
		summary.Outcome = "FAIL_INVALID_ENV"
		return
	}
	summary.InjectionsApplied = injectionsAppliedLabel(rules)
	for _, r := range rules {
		if r.AddLatency > summary.MaxInjectedLatency {
			summary.MaxInjectedLatency = r.AddLatency
		}
		if r.Timeout > summary.MaxInjectedTimeout {
			summary.MaxInjectedTimeout = r.Timeout
		}
	}

	// Do not append observed replay traffic into the captured outbound incident log.
	stub, err := stubproxy.New(input.OutboundLog, "", rules)
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
			if err := stub.ServeTransparent(listener); err != nil && !isExpectedShutdownErr(err) {
				log.Printf("Stub proxy error: %v", err)
			}
			return
		}
		stubServer := &http.Server{Handler: stub}
		if err := stubServer.Serve(listener); err != nil && !isExpectedShutdownErr(err) {
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
					if err := stubServer.Serve(compatListener); err != nil && !isExpectedShutdownErr(err) {
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
		stub.ConfigureReplayCardinality(input.Fanout > 1, expectedOutboundPerReplay*input.Fanout)
		runStart := time.Now()
		remaining := input.MaxWallTime - time.Since(start)
		if remaining <= 0 {
			summary.PrimaryFailureReason = "Replay exceeded max wall time before run start"
			break
		}

		type replayWaveResult struct {
			result replaydriver.ReplayResult
			err    error
		}
		results := make(chan replayWaveResult, input.Fanout)
		var wg sync.WaitGroup
		for worker := 0; worker < input.Fanout; worker++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
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
						SafeMode:     input.SafeMode,
					},
				)
				results <- replayWaveResult{result: r, err: err}
			}()
		}
		wg.Wait()
		close(results)
		summary.RunsExecuted++
		waveComplete := true
		waveInbound := 0
		for wr := range results {
			if wr.err != nil {
				summary.PrimaryFailureReason = fmt.Sprintf("Replay failed: %v", wr.err)
				summary.Outcome = "FAIL_STALLED"
				waveComplete = false
				continue
			}
			if wr.result.TimeExpanded || wr.result.Stalled {
				summary.PrimaryFailureReason = wr.result.TimeExpandedReason
				if wr.result.Stalled {
					summary.PrimaryFailureReason = wr.result.StalledReason
				}
				summary.Outcome = "FAIL_STALLED"
				waveComplete = false
			}
			waveInbound += wr.result.CompletedEvents
			if !referenceSet {
				referenceFingerprint = wr.result.Fingerprint
				referenceSet = true
			} else if wr.result.Fingerprint != referenceFingerprint {
				nonDeterministic = true
			}

			if input.Diff && len(wr.result.ReplayedEvents) > 0 {
				for idx, replayed := range wr.result.ReplayedEvents {
					if idx < len(events) {
						d := replaydriver.CompareEvents(events[idx], replayed, idx+1)
						if d != nil {
							summary.DiffResults = append(summary.DiffResults, d)
						}
					}
				}
			}
		}
		summary.InboundEventsReplayed += waveInbound
		outboundObserved := stub.ObservedCount()
		summary.OutboundEventsObserved += outboundObserved
		summary.DependenciesExercised = summary.OutboundEventsObserved > 0
		if stub.ForwardErrors() > 0 {
			summary.PrimaryFailureReason = "Proxy forwarding failed"
			summary.Outcome = "FAIL_PROXY_FORWARDING"
			break
		}
		if waveComplete {
			summary.RunsCompleted++
		} else {
			break
		}

		outcome := ReplayOutcome{
			RunIndex:        i + 1,
			TotalEvents:     len(events) * input.Fanout,
			CompletedEvents: waveInbound,
			WallTime:        time.Since(runStart),
			Completed:       waveComplete,
			Detail:          fmt.Sprintf("fanout=%d", input.Fanout),
		}
		summary.Outcomes = append(summary.Outcomes, outcome)

	}
	if input.Diff {
		replaydriver.PrintDiffs(summary.DiffResults)
	}
	summary.Elapsed = time.Since(start)
	if summary.Elapsed > 0 {
		summary.AchievedRPS = float64(summary.InboundEventsReplayed) / summary.Elapsed.Seconds()
	}
	if summary.Window > 0 {
		summary.TargetRPS = float64(summary.TargetInbound) / summary.Window.Seconds()
		if summary.InboundEventsReplayed < summary.TargetInbound || summary.Elapsed > summary.Window {
			summary.Outcome = "FAIL_SLO_MISSED"
			if summary.PrimaryFailureReason == "" {
				summary.PrimaryFailureReason = fmt.Sprintf(
					"SLO miss: inbound replayed %d/%d in %s (window %s, achieved %.2f req/s, target %.2f req/s)",
					summary.InboundEventsReplayed,
					summary.TargetInbound,
					summary.Elapsed.Round(time.Millisecond),
					summary.Window,
					summary.AchievedRPS,
					summary.TargetRPS,
				)
			}
		}
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
	if summary.Window > 0 && summary.InboundEventsReplayed < summary.TargetInbound {
		return "FAIL_SLO_MISSED"
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
	if summary.Window > 0 && summary.InboundEventsReplayed < summary.TargetInbound {
		gaps = append(gaps, "Replay SLO not met for requested window")
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
		fmt.Sprintf("Fanout: %d", summary.Fanout),
		fmt.Sprintf("Window: %s", summary.Window),
		fmt.Sprintf("Deterministic runs: %d / %d", summary.DeterministicRuns, summary.RunsExecuted),
		fmt.Sprintf("Inbound events replayed: %d", summary.InboundEventsReplayed),
		fmt.Sprintf("Inbound target: %d", summary.TargetInbound),
		fmt.Sprintf("Outbound events observed: %d", summary.OutboundEventsObserved),
		fmt.Sprintf("Outbound events expected: %d", summary.OutboundEventsExpected),
		fmt.Sprintf("Outbound target: %d", summary.TargetOutbound),
		fmt.Sprintf("Outbound verification: %s", summary.OutboundVerification),
		fmt.Sprintf("Elapsed: %s", summary.Elapsed.Round(time.Millisecond)),
		fmt.Sprintf("Achieved rate (req/s): %.2f", summary.AchievedRPS),
		fmt.Sprintf("Target rate (req/s): %.2f", summary.TargetRPS),
		fmt.Sprintf("Stub proxy status: %s", summary.ProxyStatus),
		fmt.Sprintf("Injections applied: %s", summary.InjectionsApplied),
		fmt.Sprintf("Dependencies exercised: %t", summary.DependenciesExercised),
		fmt.Sprintf("Primary failure reason: %s", primaryFailureOrNone(summary.PrimaryFailureReason)),
		fmt.Sprintf("Actionable recommendation: %s", summary.Recommendation),
		fmt.Sprintf("Limiting factor: %s", summary.LimitingFactor),
		"",
		"SUSTAINABLE ENVELOPE (observed)",
		fmt.Sprintf("- Max stable inbound rate: %s", summary.EnvelopeInboundRPS),
		fmt.Sprintf("- Max stable fanout: %s", summary.EnvelopeFanout),
		fmt.Sprintf("- Dependency p95 latency tolerance: %s", summary.EnvelopeLatency),
		"",
		"Change from last run:",
		fmt.Sprintf("- Fanout: %s", summary.DeltaFanout),
		fmt.Sprintf("- Achieved rate: %s", summary.DeltaRate),
		fmt.Sprintf("- Outbound completion: %s", summary.DeltaOutbound),
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
	case "FAIL_SLO_MISSED":
		return "Lower fanout or increase window; then inspect app saturation limits and outbound dependency latency."
	default:
		return "Inspect logs for additional details."
	}
}

func deriveLimitingFactor(summary *ReplaySummary) string {
	if summary.Outcome == "PASS_STRONG" || summary.Outcome == "PASS_WEAK" {
		return "NONE"
	}
	if summary.MaxInjectedTimeout > 0 {
		return "DEPENDENCY_TIMEOUT"
	}
	if summary.MaxInjectedLatency > 0 &&
		(summary.OutboundEventsObserved < summary.InboundEventsReplayed ||
			strings.Contains(summary.PrimaryFailureReason, "wall-clock")) {
		return "OUTBOUND_DEPENDENCY_LATENCY"
	}
	if summary.OutboundEventsObserved == 0 && summary.ProxyStatus == "BOUND" {
		return "PROXY_BACKPRESSURE"
	}
	if summary.OutboundEventsObserved > 0 && summary.OutboundEventsObserved < summary.InboundEventsReplayed {
		return "CONNECTION_POOL_EXHAUSTION"
	}
	return "APPLICATION_CPU"
}

func deriveEnvelope(summary *ReplaySummary) {
	summary.EnvelopeInboundRPS = "unknown"
	summary.EnvelopeFanout = "unknown"
	summary.EnvelopeLatency = "unknown"

	if summary.Outcome == "PASS_STRONG" {
		summary.EnvelopeInboundRPS = fmt.Sprintf("~%.2f req/s", summary.AchievedRPS)
		summary.EnvelopeFanout = fmt.Sprintf("~%d", summary.Fanout)
		if summary.MaxInjectedLatency > 0 {
			summary.EnvelopeLatency = fmt.Sprintf("~%s", summary.MaxInjectedLatency)
		} else {
			summary.EnvelopeLatency = "baseline only (no latency injection in this run)"
		}
		return
	}

	if summary.PreviousRun != nil && strings.HasPrefix(summary.PreviousRun.Outcome, "PASS") {
		summary.EnvelopeInboundRPS = fmt.Sprintf("~%.2f req/s (from previous pass)", summary.PreviousRun.AchievedRPS)
		summary.EnvelopeFanout = fmt.Sprintf("~%d (from previous pass)", summary.PreviousRun.Fanout)
		if summary.PreviousRun.MaxLatencyMS > 0 {
			summary.EnvelopeLatency = fmt.Sprintf("~%dms (from previous pass)", summary.PreviousRun.MaxLatencyMS)
		}
	}
}

func deriveDelta(summary *ReplaySummary) {
	summary.DeltaFanout = "n/a (no previous run)"
	summary.DeltaRate = "n/a (no previous run)"
	summary.DeltaOutbound = "n/a (no previous run)"

	prev := summary.PreviousRun
	if prev == nil {
		return
	}

	fDelta := summary.Fanout - prev.Fanout
	summary.DeltaFanout = fmt.Sprintf("%+d", fDelta)

	if prev.AchievedRPS > 0 {
		ratePct := ((summary.AchievedRPS - prev.AchievedRPS) / prev.AchievedRPS) * 100.0
		summary.DeltaRate = fmt.Sprintf("%+.1f%%", ratePct)
	}

	currComp := completionRatio(summary.OutboundEventsObserved, summary.TargetOutbound)
	prevComp := completionRatio(prev.OutboundObserved, prev.OutboundTarget)
	if prevComp > 0 {
		compPct := ((currComp - prevComp) / prevComp) * 100.0
		summary.DeltaOutbound = fmt.Sprintf("%+.1f%%", compPct)
	}
}

func completionRatio(observed, target int) float64 {
	if target <= 0 {
		return 0
	}
	return float64(observed) / float64(target)
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
	case "FAIL_SLO_MISSED":
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

func isExpectedShutdownErr(err error) bool {
	if err == nil {
		return false
	}
	if err == http.ErrServerClosed || errors.Is(err, net.ErrClosed) {
		return true
	}
	return strings.Contains(err.Error(), "use of closed network connection")
}

func replaySnapshotPath() string {
	return ".infernosim_last_run.json"
}

func loadReplaySnapshot() *ReplaySnapshot {
	b, err := os.ReadFile(replaySnapshotPath())
	if err != nil {
		return nil
	}
	var s ReplaySnapshot
	if err := json.Unmarshal(b, &s); err != nil {
		return nil
	}
	return &s
}

func saveReplaySnapshot(summary *ReplaySummary) {
	s := ReplaySnapshot{
		Timestamp:        time.Now().UTC(),
		Outcome:          summary.Outcome,
		Fanout:           summary.Fanout,
		AchievedRPS:      summary.AchievedRPS,
		OutboundObserved: summary.OutboundEventsObserved,
		OutboundTarget:   summary.TargetOutbound,
		MaxLatencyMS:     summary.MaxInjectedLatency.Milliseconds(),
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(replaySnapshotPath(), b, 0644)
}

// ---------------------------------------------------------------------------
// infernosim record
// ---------------------------------------------------------------------------

func runRecord(args []string) int {
	fs := flag.NewFlagSet("record", flag.ContinueOnError)
	listen := fs.String("listen", ":8080", "Listen address for inbound proxy")
	forward := fs.String("forward", "", "Backend host:port to forward to (required)")
	out := fs.String("out", "./incident", "Output directory for the incident bundle")
	env := fs.String("env", "", "Environment label (e.g. production, staging)")
	httpsMode := fs.String("https-mode", "tunnel", "HTTPS mode: tunnel or mitm")

	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(os.Stderr, "record: %v\n", err)
		return 1
	}
	if *forward == "" {
		fmt.Fprintln(os.Stderr, "record: --forward host:port is required")
		return 1
	}

	if err := os.MkdirAll(*out, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "record: create output dir: %v\n", err)
		return 1
	}

	inboundLogPath := filepath.Join(*out, "inbound.log")
	outboundLogPath := filepath.Join(*out, "outbound.log")

	inboundLogger, err := event.NewLogger(inboundLogPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "record: open inbound log: %v\n", err)
		return 1
	}
	defer inboundLogger.Close()

	outboundLogger, err := event.NewLogger(outboundLogPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "record: open outbound log: %v\n", err)
		return 1
	}
	defer outboundLogger.Close()

	useMITM := *httpsMode == "mitm"
	var caStore *capture.CAStore
	if useMITM {
		caStore, err = capture.NewCAStore()
		if err != nil {
			fmt.Fprintf(os.Stderr, "record: CA store: %v\n", err)
			return 1
		}
	}

	ctx := &capture.ProxyContext{
		Logger:  inboundLogger,
		CA:      caStore,
		UseMITM: useMITM,
	}

	targetURL := &url.URL{Scheme: "http", Host: *forward}
	server, err := capture.StartInboundProxy(*listen, targetURL, ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "record: start proxy: %v\n", err)
		return 1
	}

	log.Printf("Recording → %s (out: %s)", *forward, *out)
	log.Printf("Press Ctrl-C to stop recording.")

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop

	_ = server.Close()
	_ = inboundLogger.Close()
	_ = outboundLogger.Close()

	// Count captured events for metadata.
	inboundCount := countEvents(inboundLogPath, "InboundRequest")
	outboundCount := countEvents(outboundLogPath, "")

	host := *forward
	meta := replaydriver.IncidentMetadata{
		CapturedAt:    time.Now().UTC(),
		Env:           *env,
		Host:          host,
		Listen:        *listen,
		Forward:       *forward,
		InboundCount:  inboundCount,
		OutboundCount: outboundCount,
	}
	if err := replaydriver.WriteMetadata(*out, meta); err != nil {
		fmt.Fprintf(os.Stderr, "record: write incident.json: %v\n", err)
		return 1
	}

	fmt.Printf("\nIncident saved to %s\n", *out)
	fmt.Printf("  inbound events:  %d\n", inboundCount)
	fmt.Printf("  outbound events: %d\n", outboundCount)
	return 0
}

// countEvents counts JSONL lines in a log file, optionally filtered by event type.
func countEvents(path, eventType string) int {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()
	dec := json.NewDecoder(f)
	count := 0
	for {
		var e event.Event
		if err := dec.Decode(&e); err != nil {
			break
		}
		if eventType == "" || e.Type == eventType {
			count++
		}
	}
	return count
}

// ---------------------------------------------------------------------------
// infernosim inspect
// ---------------------------------------------------------------------------

func runInspect(args []string) int {
	fs := flag.NewFlagSet("inspect", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil || fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "Usage: infernosim inspect <incident-dir>")
		return 1
	}
	dir := fs.Arg(0)

	bundle, err := replaydriver.OpenBundle(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "inspect: %v\n", err)
		return 1
	}

	result, err := replaydriver.InspectIncident(bundle.InboundLog)
	if err != nil {
		fmt.Fprintf(os.Stderr, "inspect: %v\n", err)
		return 1
	}

	if meta, err := bundle.ReadMetadata(); err == nil && !meta.CapturedAt.IsZero() {
		fmt.Printf("Captured: %s  Env: %s  Host: %s\n\n", meta.CapturedAt.Format(time.RFC3339), meta.Env, meta.Host)
	}

	replaydriver.PrintInspectResult(result)
	return 0
}

// ---------------------------------------------------------------------------
// infernosim verify
// ---------------------------------------------------------------------------

func runVerify(args []string) int {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil || fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "Usage: infernosim verify <incident-dir>")
		return 1
	}
	dir := fs.Arg(0)

	bundle, err := replaydriver.OpenBundle(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "verify: %v\n", err)
		return 1
	}

	result, err := replaydriver.VerifyIncident(bundle.InboundLog)
	if err != nil {
		fmt.Fprintf(os.Stderr, "verify: %v\n", err)
		return 1
	}

	replaydriver.PrintVerifyResult(result)

	if result.ReadinessScore < 60 {
		return 1
	}
	return 0
}

// ---------------------------------------------------------------------------
// infernosim diff
// ---------------------------------------------------------------------------

func runDiff(args []string) int {
	fs := flag.NewFlagSet("diff", flag.ContinueOnError)
	targetBase := fs.String("target", "http://localhost:8080", "Target base URL to replay against")
	maxWall := fs.Duration("max-wall-time", 60*time.Second, "Max wall-clock time for the replay run")

	if err := fs.Parse(args); err != nil || fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "Usage: infernosim diff <incident-dir> [--target http://...]")
		return 1
	}
	dir := fs.Arg(0)

	bundle, err := replaydriver.OpenBundle(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "diff: %v\n", err)
		return 1
	}

	// If a replay.yaml exists, load target from it (CLI flag takes precedence if explicitly provided).
	if bundle.HasConfig() {
		cfg, err := replaydriver.LoadReplayConfig(bundle.ConfigPath)
		if err == nil && cfg.Target != "" && *targetBase == "http://localhost:8080" {
			*targetBase = cfg.Target
		}
	}

	events, err := replaydriver.LoadInboundEvents(bundle.InboundLog)
	if err != nil {
		fmt.Fprintf(os.Stderr, "diff: load events: %v\n", err)
		return 1
	}

	result, err := replaydriver.ReplayEvents(events, *targetBase, replaydriver.ReplayConfig{
		TimeScale:    1.0,
		Density:      1.0,
		MinGap:       2 * time.Millisecond,
		MaxWallClock: *maxWall,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "diff: replay: %v\n", err)
		return 1
	}

	var diffs []*replaydriver.EventDiff
	for i, replayed := range result.ReplayedEvents {
		if i < len(events) {
			if d := replaydriver.CompareEvents(events[i], replayed, i+1); d != nil {
				diffs = append(diffs, d)
			}
		}
	}

	fmt.Printf("Diff Summary\n")
	fmt.Printf("------------\n")
	fmt.Printf("Requests replayed: %d\n", result.CompletedEvents)
	fmt.Printf("Divergences found: %d\n", len(diffs))
	if result.SafeModeSkipped > 0 {
		fmt.Printf("Safe-mode skipped: %d\n", result.SafeModeSkipped)
	}
	fmt.Println()
	replaydriver.PrintDiffs(diffs)

	if len(diffs) > 0 {
		return 1
	}
	return 0
}
