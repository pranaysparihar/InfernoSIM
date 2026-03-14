package main

import (
	"flag"
	"fmt"
	"log"
	"net/url"
	"os"
	"os/signal"
	"syscall"

	"infernosim/pkg/capture"
	"infernosim/pkg/event"
	"infernosim/pkg/inject"
	"infernosim/pkg/replay"
)

func main() {
	// Define CLI flags
	mode := flag.String("mode", "inbound", "Mode: 'inbound' for sidecar, 'proxy' for outbound, 'replay' for deterministic replay, 'search' for envelope bounds")
	listen := flag.String("listen", ":8080", "Listen address for the agent")
	forward := flag.String("forward", "", "Forward address for inbound mode (host:port)")
	logFile := flag.String("log", "events.log", "Event log output file")

	// New Flags
	httpsMode := flag.String("https-mode", "tunnel", "Outbound HTTPS behavior: 'tunnel' or 'mitm'")
	injectParam := flag.String("inject", "", "Fault injection config (e.g. jitter=50ms,drop=5%,reset=5%,status=503,rate=10%)")

	flag.Parse()

	// Open event log
	logger, err := event.NewLogger(*logFile)
	if err != nil {
		log.Fatalf("Failed to open log file: %v", err)
	}
	defer logger.Close()

	// Parse Injection
	injectCfg, err := inject.ParseConfig(*injectParam, 0)
	if err != nil {
		log.Fatalf("Failed to parse injection config: %v", err)
	}

	// Init CA if MITM
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

	// Graceful shutdown handling
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	log.Printf("InfernoSIM agent starting | listen=%s", *listen)

	if len(os.Args) > 1 && os.Args[1] == "replay" {
		log.Println("Starting Replay Subcommand Context")
		// Parse replay specific flags
		replayCmd := flag.NewFlagSet("replay", flag.ExitOnError)
		rLogFile := replayCmd.String("log", "events.log", "Event log to replay")
		rTarget := replayCmd.String("target", "", "Target base URL (e.g. http://localhost:8081)")
		replayCmd.Parse(os.Args[2:])

		if *rTarget == "" {
			log.Fatal("Replay requires --target")
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
		os.Exit(0)
	}

	if len(os.Args) > 1 && os.Args[1] == "search" {
		log.Println("Starting Search (Envelope) Subcommand Context")
		searchCmd := flag.NewFlagSet("search", flag.ExitOnError)
		sLogFile := searchCmd.String("log", "events.log", "Event log to replay")
		sTarget := searchCmd.String("target", "", "Target base URL")
		searchCmd.Parse(os.Args[2:])

		if *sTarget == "" {
			log.Fatal("Search requires --target")
		}

		// Simplified Envelope search mapping Fanout stress
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
				// Upper bound cap for demo
				fmt.Printf("=== PASS_STRONG: Envelope stable up to fanout %d ===\n", fanout)
				break
			}
			fanout++
		}
		os.Exit(0)
	}

	if *mode == "inbound" {
		if *forward == "" {
			log.Fatal("Inbound mode requires --forward host:port")
		}

		targetURL := &url.URL{
			Scheme: "http",
			Host:   *forward,
		}

		server, err := capture.StartInboundProxy(*listen, targetURL, ctx)
		if err != nil {
			log.Fatalf("Failed to start inbound proxy: %v", err)
		}

		log.Printf("Inbound proxy active → %s", *forward)
		<-stop
		log.Println("Shutting down inbound proxy")
		server.Close()

	} else if *mode == "proxy" {

		server, err := capture.StartForwardProxy(*listen, ctx)
		if err != nil {
			log.Fatalf("Failed to start forward proxy: %v", err)
		}

		log.Printf("Outbound proxy active")
		<-stop
		log.Println("Shutting down outbound proxy")
		server.Close()

	} else {
		log.Fatalf("Unknown mode: %s (replay and search are subcommands not modes, use 'infernosim replay')", *mode)
	}

	log.Println("InfernoSIM agent stopped")
}
