package replaydriver

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/cookiejar"
	urlpkg "net/url"
	"os"
	"sort"
	"strings"
	"time"

	"infernosim/pkg/event"
)

type ReplayResult struct {
	Fingerprint        [32]byte
	CompletedEvents    int
	TotalEvents        int
	LastProgressIndex  int
	RunDuration        time.Duration
	ExpectedDuration   time.Duration
	ResponseSignatures []string
	ErrorCount         int
	SafeModeSkipped    int
	TimeExpanded       bool
	TimeExpandedReason string
	ReplayedEvents     []event.Event
	Stalled            bool
	StalledReason      string
}

type ReplayConfig struct {
	TimeScale     float64
	Density       float64
	MinGap        time.Duration
	MaxWallClock  time.Duration
	MaxIdleTime   time.Duration
	MaxEvents     int
	SafeMode      bool
	SafeModeAllow []string // URL path prefixes allowed even in safe mode
	StateAdapters []StateAdapter
	ChaosDelay    time.Duration
	ChaosRequest  int // 1-based request index; 0 applies to every request
}

func LoadInboundEvents(inboundLog string) ([]event.Event, error) {
	f, err := os.Open(inboundLog)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	dec := json.NewDecoder(f)
	var evs []event.Event
	responses := make(map[string]event.Event)

	for {
		var e event.Event
		if err := dec.Decode(&e); err != nil {
			if err == io.EOF {
				break
			}
			return nil, err
		}
		if e.Type == "InboundRequest" {
			evs = append(evs, e)
		} else if e.Type == "InboundResponse" && e.TraceID != "" {
			responses[e.TraceID] = e
		}
	}
	for i := range evs {
		resp, ok := responses[evs[i].TraceID]
		if !ok {
			continue
		}
		evs[i].Status = resp.Status
		evs[i].Duration = resp.Timestamp.Sub(evs[i].Timestamp)
		evs[i].ResponseCaptured = true
		evs[i].ResponseHeaders = resp.Headers
		evs[i].ResponseBodyB64 = resp.BodyB64
		evs[i].ResponseBodySha256 = resp.BodySha256
		evs[i].ResponseBodyTruncated = resp.BodyTruncated
		evs[i].ResponseBodyRedacted = resp.BodyRedacted
	}
	// Sort by (Timestamp, Sequence) for deterministic ordering under concurrent capture.
	sort.SliceStable(evs, func(i, j int) bool {
		if evs[i].Timestamp.Equal(evs[j].Timestamp) {
			return evs[i].Sequence < evs[j].Sequence
		}
		return evs[i].Timestamp.Before(evs[j].Timestamp)
	})
	return evs, nil
}

// isSideEffect returns true for HTTP methods that modify server state.
func isSideEffect(method string) bool {
	switch strings.ToUpper(method) {
	case "POST", "PUT", "PATCH", "DELETE":
		return true
	}
	return false
}

// safeModeAllowed returns true if the path matches one of the allow prefixes.
func safeModeAllowed(path string, allow []string) bool {
	for _, prefix := range allow {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}

func ExpectedDuration(events []event.Event, timeScale float64, density float64, minGap time.Duration) time.Duration {
	if len(events) == 0 {
		return 0
	}
	prevTS := events[0].Timestamp
	var total time.Duration
	for _, e := range events {
		rawGap := e.Timestamp.Sub(prevTS)
		if rawGap < 0 {
			rawGap = 0
		}

		gap := time.Duration(float64(rawGap) * timeScale / density)
		if gap < minGap {
			gap = minGap
		}
		total += gap
		prevTS = e.Timestamp
	}
	return total
}

// Replay deterministically replays inbound requests.
// density=1 preserves original gaps.
// density>1 collapses time proportionally.
// minGap prevents zero-gap busy loops.
func ReplayEvents(
	events []event.Event,
	targetBase string,
	cfg ReplayConfig,
) (ReplayResult, error) {

	if cfg.TimeScale <= 0 {
		return ReplayResult{}, fmt.Errorf("time-scale must be > 0")
	}
	if cfg.Density <= 0 {
		return ReplayResult{}, fmt.Errorf("density must be > 0")
	}

	if cfg.MaxEvents > 0 && len(events) > cfg.MaxEvents {
		events = events[:cfg.MaxEvents]
	}

	if len(events) == 0 {
		return ReplayResult{}, fmt.Errorf("no inbound requests")
	}

	expected := ExpectedDuration(events, cfg.TimeScale, cfg.Density, cfg.MinGap)

	h := sha256.New()
	jar, _ := cookiejar.New(nil)
	client := &http.Client{
		Timeout: 15 * time.Second,
		Jar:     jar,
	}

	state := NewRuntimeState()
	if len(cfg.StateAdapters) > 0 {
		if err := ApplyAdapters(state, cfg.StateAdapters); err != nil {
			log.Printf("state adapters warning: %v", err)
		}
	}
	rewriter := NewRequestRewriterWithEvents(state, events)

	start := time.Now()
	deadline := time.Time{}
	if cfg.MaxWallClock > 0 {
		deadline = start.Add(cfg.MaxWallClock)
	}

	lastProgress := start
	lastProgressIndex := 0

	prevTS := events[0].Timestamp
	nextAt := time.Now()
	signatures := make([]string, 0, len(events))
	replayedEvents := make([]event.Event, 0, len(events))
	errCount := 0
	safeModeSkipped := 0

	for i, e := range events {
		startTime := time.Now()
		if cfg.MaxIdleTime > 0 && time.Since(lastProgress) > cfg.MaxIdleTime {
			return ReplayResult{
				CompletedEvents:    i,
				TotalEvents:        len(events),
				LastProgressIndex:  lastProgressIndex,
				RunDuration:        time.Since(start),
				ExpectedDuration:   expected,
				ResponseSignatures: signatures,
				ErrorCount:         errCount,
				Stalled:            true,
				StalledReason:      "no replay progress observed within idle limit",
			}, nil
		}
		rawGap := e.Timestamp.Sub(prevTS)
		if rawGap < 0 {
			rawGap = 0
		}

		// Apply time-scale first, then density
		gap := time.Duration(float64(rawGap) * cfg.TimeScale / cfg.Density)

		if gap < cfg.MinGap {
			gap = cfg.MinGap
		}

		nextAt = nextAt.Add(gap)

		if wait := time.Until(nextAt); wait > 0 {
			time.Sleep(wait)
		}

		if !deadline.IsZero() && time.Now().After(deadline) {
			return ReplayResult{
				CompletedEvents:    i,
				TotalEvents:        len(events),
				LastProgressIndex:  lastProgressIndex,
				RunDuration:        time.Since(start),
				ExpectedDuration:   expected,
				ResponseSignatures: signatures,
				ErrorCount:         errCount,
				TimeExpanded:       true,
				TimeExpandedReason: "replay exceeded wall-clock limit while preserving timing",
			}, nil
		}

		prevTS = e.Timestamp

		if cfg.ChaosDelay > 0 && (cfg.ChaosRequest == 0 || cfg.ChaosRequest == i+1) {
			time.Sleep(cfg.ChaosDelay)
		}

		if i == 0 || i%10 == 0 {
			log.Printf(
				"Replay %d/%d | rawGap=%s scaledGap=%s density=%.1f",
				i+1, len(events), rawGap, gap, cfg.Density,
			)
		}

		parsed, err := urlpkg.Parse(e.URL)
		if err != nil {
			return ReplayResult{}, err
		}

		var body io.Reader
		bodyBytes, hasBody := rewriter.PrepareBody(e)
		if hasBody {
			body = bytes.NewReader(bodyBytes)
		} else if e.BodyRedacted && e.BodySize > 0 && !cfg.SafeMode {
			return ReplayResult{}, fmt.Errorf(
				"request %d body was omitted during secure capture; recapture with --capture-sensitive-data before replaying writes",
				i+1,
			)
		}

		target, err := urlpkg.Parse(targetBase)
		if err != nil || target.Scheme == "" || target.Host == "" {
			return ReplayResult{}, fmt.Errorf("invalid target base %q", targetBase)
		}
		target.Path = parsed.Path
		target.RawPath = parsed.RawPath
		target.RawQuery = parsed.RawQuery
		req, err := http.NewRequest(e.Method, target.String(), body)
		if err != nil {
			return ReplayResult{}, err
		}

		// Replay captured headers
		for k, vals := range e.Headers {
			if shouldSkipReplayHeader(k) {
				continue
			}
			for _, v := range vals {
				if v == "[REDACTED]" {
					continue
				}
				req.Header.Add(k, v)
			}
		}

		// Apply state-aware substitutions
		rewriter.Rewrite(e, req)

		// Safe mode: skip side-effect requests unless explicitly allowed
		if cfg.SafeMode && isSideEffect(req.Method) && !safeModeAllowed(req.URL.Path, cfg.SafeModeAllow) {
			log.Printf("[safe-mode] skipped %s %s", req.Method, req.URL.Path)
			safeModeSkipped++
			lastProgress = time.Now()
			lastProgressIndex = i + 1
			prevTS = e.Timestamp
			continue
		}

		reqTimeout := client.Timeout
		if cfg.MaxIdleTime > 0 && (reqTimeout == 0 || cfg.MaxIdleTime < reqTimeout) {
			reqTimeout = cfg.MaxIdleTime
		}
		var cancel context.CancelFunc
		if reqTimeout > 0 {
			ctx, c := context.WithTimeout(context.Background(), reqTimeout)
			cancel = c
			req = req.WithContext(ctx)
		}

		resp, err := client.Do(req)
		if cancel != nil {
			cancel()
		}
		if err != nil {
			errCount++
			sig := fmt.Sprintf("%s %s ERR:%T", e.Method, parsed.RequestURI(), err)
			signatures = append(signatures, sig)
			h.Write([]byte(sig))
			lastProgress = time.Now()
			lastProgressIndex = i + 1
			if !deadline.IsZero() && time.Now().After(deadline) {
				return ReplayResult{
					CompletedEvents:    i + 1,
					TotalEvents:        len(events),
					LastProgressIndex:  lastProgressIndex,
					RunDuration:        time.Since(start),
					ExpectedDuration:   expected,
					ResponseSignatures: signatures,
					ErrorCount:         errCount,
					TimeExpanded:       true,
					TimeExpandedReason: "replay exceeded wall-clock limit while awaiting responses",
				}, nil
			}
			continue
		}
		respBody, readErr := io.ReadAll(io.LimitReader(resp.Body, 16*1024*1024+1))
		resp.Body.Close()
		if readErr != nil {
			return ReplayResult{}, fmt.Errorf("read replay response: %w", readErr)
		}
		if len(respBody) > 16*1024*1024 {
			return ReplayResult{}, fmt.Errorf("replay response exceeds 16 MiB safety limit")
		}
		respHash := sha256.Sum256(respBody)

		replayedEvt := event.Event{
			Method:             req.Method,
			URL:                req.URL.String(),
			Status:             resp.StatusCode,
			Headers:            resp.Header.Clone(),
			BodySha256:         fmt.Sprintf("%x", respHash),
			Duration:           time.Since(startTime),
			Timestamp:          e.Timestamp, // preserve captured timestamp for apples-to-apples diff
			ResponseCaptured:   true,
			ResponseHeaders:    resp.Header.Clone(),
			ResponseBodySha256: fmt.Sprintf("%x", respHash),
		}
		replayedEvents = append(replayedEvents, replayedEvt)

		// Update state from response
		rewriter.UpdateState(e, resp, respBody)

		sig := fmt.Sprintf(
			"%s %s %d body=%x content-type=%q location=%q",
			e.Method,
			parsed.RequestURI(),
			resp.StatusCode,
			respHash,
			resp.Header.Get("Content-Type"),
			resp.Header.Get("Location"),
		)
		signatures = append(signatures, sig)
		h.Write([]byte(sig))
		lastProgress = time.Now()
		lastProgressIndex = i + 1
		if !deadline.IsZero() && time.Now().After(deadline) {
			return ReplayResult{
				CompletedEvents:    i + 1,
				TotalEvents:        len(events),
				LastProgressIndex:  lastProgressIndex,
				RunDuration:        time.Since(start),
				ExpectedDuration:   expected,
				ResponseSignatures: signatures,
				ErrorCount:         errCount,
				TimeExpanded:       true,
				TimeExpandedReason: "replay exceeded wall-clock limit while awaiting responses",
			}, nil
		}
	}

	var out [32]byte
	copy(out[:], h.Sum(nil))
	return ReplayResult{
		Fingerprint:        out,
		CompletedEvents:    len(events) - safeModeSkipped,
		TotalEvents:        len(events),
		LastProgressIndex:  lastProgressIndex,
		RunDuration:        time.Since(start),
		ExpectedDuration:   expected,
		ResponseSignatures: signatures,
		ReplayedEvents:     replayedEvents,
		ErrorCount:         errCount,
		SafeModeSkipped:    safeModeSkipped,
	}, nil
}

func shouldSkipReplayHeader(name string) bool {
	switch http.CanonicalHeaderKey(name) {
	case "Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization",
		"Proxy-Connection", "Te", "Trailer", "Transfer-Encoding", "Upgrade",
		"Content-Length":
		return true
	default:
		return false
	}
}
