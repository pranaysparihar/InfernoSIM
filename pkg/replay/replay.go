package replay

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"infernosim/pkg/event"
)

// ReplayConfig controls how a replay is executed
type ReplayConfig struct {
	Strict bool // if true, any unexpected behavior aborts replay
}

// Replayer executes a deterministic replay from an event log
type Replayer struct {
	events []event.Event
	config ReplayConfig
}

// NewReplayer loads events from a log file
func NewReplayer(logFile string, config ReplayConfig) (*Replayer, error) {
	file, err := os.Open(logFile)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var events []event.Event
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var evt event.Event
		if err := json.Unmarshal(scanner.Bytes(), &evt); err != nil {
			return nil, fmt.Errorf("failed to parse event: %w", err)
		}
		events = append(events, evt)
	}

	if len(events) == 0 {
		return nil, errors.New("no events found in log")
	}

	return &Replayer{
		events: events,
		config: config,
	}, nil
}

// Replay executes the event sequence deterministically
func (r *Replayer) Replay() error {
	fmt.Println("=== InfernoSIM Replay Started ===")

	var lastTime time.Time

	for i, evt := range r.events {
		// Enforce ordering by timestamp
		if !lastTime.IsZero() && evt.Timestamp.Before(lastTime) {
			return r.divergence(i, "event timestamp moved backwards")
		}
		lastTime = evt.Timestamp

		if err := r.applyEvent(evt); err != nil {
			return r.divergence(i, err.Error())
		}
	}

	fmt.Println("=== InfernoSIM Replay Completed Successfully ===")
	return nil
}

// applyEvent applies a single event during replay
func (r *Replayer) applyEvent(evt event.Event) error {
	switch evt.Type {

	case "InboundRequest":
		fmt.Printf("[REPLAY] Inbound request %s %s\n", evt.Method, evt.URL)

	case "InboundResponse":
		fmt.Printf("[REPLAY] Inbound response %d for %s\n", evt.Status, evt.URL)

	case "OutboundCall":
		fmt.Printf("[REPLAY] Outbound call %s %s (status=%d, duration=%s)\n",
			evt.Method,
			evt.URL,
			evt.Status,
			evt.Duration,
		)

	case "Timeout":
		fmt.Printf("[REPLAY] Timeout occurred: %s\n", evt.Error)

	case "Retry":
		fmt.Printf("[REPLAY] Retry triggered for %s\n", evt.URL)

	default:
		if r.config.Strict {
			return fmt.Errorf("unknown event type: %s", evt.Type)
		}
		fmt.Printf("[REPLAY] Skipping unknown event type: %s\n", evt.Type)
	}

	return nil
}

// divergence aborts replay and reports the first divergence point
func (r *Replayer) divergence(index int, reason string) error {
	evt := r.events[index]
	return fmt.Errorf(
		"REPLAY DIVERGENCE at event #%d (type=%s, id=%s): %s",
		index,
		evt.Type,
		evt.ID,
		reason,
	)
}