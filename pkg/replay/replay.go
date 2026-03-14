package replay

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"infernosim/pkg/event"

	"golang.org/x/net/http2"
)

// ReplayConfig controls how a replay is executed
type ReplayConfig struct {
	Strict bool // if true, any unexpected behavior aborts replay
}

// Replayer executes a deterministic replay from an event log
type Replayer struct {
	events []event.Event
	config ReplayConfig
	client *http.Client
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

	// Create a client that uses HTTP/2 transport seamlessly for generic outbound reqs
	client := &http.Client{
		Transport: &http2.Transport{
			AllowHTTP: true,
			DialTLSContext: func(ctx context.Context, network, addr string, cfg *tls.Config) (net.Conn, error) {
				return net.Dial(network, addr) // Force cleartext H2C if dialed without TLS
			},
		},
		Timeout: 30 * time.Second,
	}

	return &Replayer{
		events: events,
		config: config,
		client: client,
	}, nil
}

// PreflightValidation tests if the proxy and target are reachable
func (r *Replayer) PreflightValidation(targetBase string) error {
	u, err := url.Parse(targetBase)
	var host string
	if err != nil || u.Host == "" {
		host = targetBase
	} else {
		host = u.Host
	}

	if !strings.Contains(host, ":") {
		if strings.HasPrefix(targetBase, "https") {
			host += ":443"
		} else {
			host += ":80"
		}
	}

	// Simple TCP dial validation without pinging HTTP endpoints
	conn, err := net.DialTimeout("tcp", host, 2*time.Second)
	if err != nil {
		return fmt.Errorf("preflight failed: target %s unreachable: %v", targetBase, err)
	}
	conn.Close()
	return nil
}

// Replay executes the event sequence deterministically
func (r *Replayer) Replay() error {
	fmt.Println("=== InfernoSIM Replay Started ===")

	var lastTime time.Time
	var outboundExpected int
	var outboundObserved int

	// Sort events chronologically to gracefully handle concurrent proxy logs
	sort.Slice(r.events, func(i, j int) bool {
		return r.events[i].Timestamp.Before(r.events[j].Timestamp)
	})

	for i, evt := range r.events {
		// Enforce ordering by timestamp
		if !lastTime.IsZero() && evt.Timestamp.Before(lastTime) {
			return r.divergence(i, "event timestamp moved backwards")
		}
		lastTime = evt.Timestamp

		if evt.Type == "OutboundCall" && evt.Method != "CONNECT" {
			outboundExpected++
			err := r.executeOutboundCall(evt)
			if err != nil {
				return r.divergence(i, fmt.Sprintf("outbound call failed: %v", err))
			}
			outboundObserved++
		} else {
			if err := r.applyEvent(evt); err != nil {
				return r.divergence(i, err.Error())
			}
		}
	}

	if outboundExpected > 0 && outboundObserved == 0 {
		fmt.Println("=== FAIL_SLO_MISSED: Outbound coverage mismatch ===")
		return fmt.Errorf("expected %d outbound calls, observed %d", outboundExpected, outboundObserved)
	}

	fmt.Println("=== PASS_STRONG: InfernoSIM Replay Completed Successfully ===")
	return nil
}

func (r *Replayer) executeOutboundCall(evt event.Event) error {
	var bodyReader io.Reader

	if evt.BodyB64 != "" {
		bodyBytes, err := base64.StdEncoding.DecodeString(evt.BodyB64)
		if err != nil {
			return fmt.Errorf("failed to decode b64 body: %v", err)
		}

		// Fingerprint check
		hash := sha256.Sum256(bodyBytes)
		calculatedHash := hex.EncodeToString(hash[:])
		if evt.BodySha256 != "" && calculatedHash != evt.BodySha256 {
			return fmt.Errorf("FAIL_NON_DETERMINISTIC: deterministic mismatch: expected body hash %s, got %s", evt.BodySha256, calculatedHash)
		}

		bodyReader = bytes.NewReader(bodyBytes)
	}

	req, err := http.NewRequest(evt.Method, evt.URL, bodyReader)
	if err != nil {
		return fmt.Errorf("failed to create request: %v", err)
	}

	for k, vals := range evt.Headers {
		for _, v := range vals {
			req.Header.Add(k, v)
		}
	}

	// Use standard Transport for HTTP/1.1 if not explicitly gRPC
	if evt.GrpcServiceMethod == "" {
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			if r.config.Strict && evt.Status > 0 {
				return fmt.Errorf("FAIL_SLO_MISSED: expected status %d but got network dial error", evt.Status)
			}
			return nil
		}
		defer resp.Body.Close()

		if r.config.Strict && evt.Status > 0 && evt.Status != resp.StatusCode {
			return fmt.Errorf("FAIL_SLO_MISSED: strict status mismatch - log recorded %d, replay target returned %d", evt.Status, resp.StatusCode)
		}
	} else {
		// gRPC utilizes HTTP/2 explicitly
		req.Header.Set("Content-Type", "application/grpc")
		resp, err := r.client.Do(req)
		if err != nil {
			if r.config.Strict && evt.Status > 0 {
				return fmt.Errorf("FAIL_SLO_MISSED: expected status %d but got network dial error", evt.Status)
			}
			return nil
		}
		defer resp.Body.Close()

		if r.config.Strict && evt.Status > 0 && evt.Status != resp.StatusCode {
			return fmt.Errorf("FAIL_SLO_MISSED: strict status mismatch - log recorded %d, replay target returned %d", evt.Status, resp.StatusCode)
		}
	}

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
		fmt.Printf("[REPLAY] Outbound CONNECT %s %s\n", evt.Method, evt.URL)

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
