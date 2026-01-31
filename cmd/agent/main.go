package main

import (
	"flag"
	"log"
	"net/url"
	"os"
	"os/signal"
	"syscall"

	"infernosim/pkg/capture"
	"infernosim/pkg/event"
)

func main() {
	// Define CLI flags
	mode := flag.String("mode", "inbound", "Mode: 'inbound' for sidecar inbound capture, 'proxy' for outbound proxy capture")
	listen := flag.String("listen", ":8080", "Listen address for the agent")
	forward := flag.String("forward", "", "Forward address for inbound mode (host:port)")
	logFile := flag.String("log", "events.log", "Event log output file")
	flag.Parse()

	// Open event log
	logger, err := event.NewLogger(*logFile)
	if err != nil {
		log.Fatalf("Failed to open log file: %v", err)
	}
	defer logger.Close()

	// Graceful shutdown handling
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	log.Printf("InfernoSIM agent starting | mode=%s listen=%s", *mode, *listen)

	if *mode == "inbound" {
		if *forward == "" {
			log.Fatal("Inbound mode requires --forward host:port")
		}

		targetURL := &url.URL{
			Scheme: "http",
			Host:   *forward,
		}

		server, err := capture.StartInboundProxy(*listen, targetURL, logger)
		if err != nil {
			log.Fatalf("Failed to start inbound proxy: %v", err)
		}

		log.Printf("Inbound proxy active → %s", *forward)
		<-stop
		log.Println("Shutting down inbound proxy")
		server.Close()

	} else if *mode == "proxy" {

		server, err := capture.StartForwardProxy(*listen, logger)
		if err != nil {
			log.Fatalf("Failed to start forward proxy: %v", err)
		}

		log.Printf("Outbound proxy active")
		<-stop
		log.Println("Shutting down outbound proxy")
		server.Close()

	} else {
		log.Fatalf("Unknown mode: %s", *mode)
	}

	log.Println("InfernoSIM agent stopped")
}