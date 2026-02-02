package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
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
		runReplay(os.Args[2:])
		return
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
func runReplay(args []string) {
	fs := flag.NewFlagSet("replay", flag.ExitOnError)

	incidentDir := fs.String(
		"incident",
		".",
		"Incident directory (contains inbound.log and outbound.log)",
	)

	timeScale := fs.Float64(
		"time-scale",
		1.0,
		"Time scale: 0.1=10x faster, 2.0=2x slower",
	)

	runs := fs.Int(
		"runs",
		10,
		"Number of replay runs",
	)

	maxGapStr := fs.String(
		"max-gap",
		"0",
		"Maximum gap between replayed events (e.g. 100ms, 1s). 0 = unlimited",
	)

	injectFlags := multiFlag{}
	fs.Var(
		&injectFlags,
		"inject",
		`Injection rule, e.g. --inject "dep=worldtimeapi.org latency=+200ms"`,
	)

	fs.Parse(args)

	// ---- parse max-gap ----
	maxGap, err := time.ParseDuration(*maxGapStr)
	if err != nil {
		log.Fatalf("Invalid --max-gap value: %v", err)
	}

	// ---- resolve logs ----
	inboundLog := filepath.Join(*incidentDir, "inbound.log")
	outboundLog := filepath.Join(*incidentDir, "outbound.log")

	if _, err := os.Stat(inboundLog); err != nil {
		log.Fatalf("Inbound log not found: %s (%v)", inboundLog, err)
	}
	if _, err := os.Stat(outboundLog); err != nil {
		log.Fatalf("Outbound log not found: %s (%v)", outboundLog, err)
	}

	// ---- parse injections ----
	rules, err := inject.ParseRules(injectFlags)
	if err != nil {
		log.Fatalf("Invalid --inject rule: %v", err)
	}

	// ---- start stub proxy ----
	stub, err := stubproxy.New(outboundLog, rules)
	if err != nil {
		log.Fatalf("Stub proxy init failed: %v", err)
	}

	stubServer := &http.Server{
		Addr:    ":19000",
		Handler: stub,
	}

	go func() {
		log.Printf("Stub proxy active on :19000")
		if err := stubServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Stub proxy error: %v", err)
		}
	}()

	// allow stub proxy to bind
	time.Sleep(300 * time.Millisecond)

	targetBase := "http://localhost:18080"

	log.Printf(
		"Starting deterministic replay | runs=%d timeScale=%.3f maxGap=%s",
		*runs,
		*timeScale,
		maxGap,
	)

	var ref [32]byte

	for i := 0; i < *runs; i++ {
		stub.Reset()

		r, err := replaydriver.Replay(
			inboundLog,
			targetBase,
			*timeScale,
			maxGap,
		)
		if err != nil {
			log.Fatalf("REPLAY FAIL: %v", err)
		}

		if i == 0 {
			ref = r.Fingerprint
		} else if r.Fingerprint != ref {
			log.Fatalf(
				"REPLAY FAIL: non-deterministic fingerprint on run %d",
				i+1,
			)
		}
	}

	// ---- persist result ----
	out := fmt.Sprintf(
		"REPLAY: PASS\nFINGERPRINT: %x\nRUNS: %d\nTIME_SCALE: %.3f\nMAX_GAP: %s\n",
		ref,
		*runs,
		*timeScale,
		maxGap,
	)

	if err := os.WriteFile("replay_result.txt", []byte(out), 0644); err != nil {
		log.Fatalf("Failed to write replay_result.txt: %v", err)
	}

	log.Printf("REPLAY PASS (deterministic, simulated)")
	_ = stubServer.Close()
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
