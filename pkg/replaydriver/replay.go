package replaydriver

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	urlpkg "net/url"
	"os"
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
	TimeExpanded       bool
	TimeExpandedReason string
	Stalled            bool
	StalledReason      string
}

type ReplayConfig struct {
	TimeScale    float64
	Density      float64
	MinGap       time.Duration
	MaxWallClock time.Duration
	MaxIdleTime  time.Duration
	MaxEvents    int
}

func LoadInboundEvents(inboundLog string) ([]event.Event, error) {
	f, err := os.Open(inboundLog)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	dec := json.NewDecoder(f)
	var evs []event.Event

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
		}
	}
	return evs, nil
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
	client := &http.Client{Timeout: 15 * time.Second}

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
	errCount := 0

	for i, e := range events {
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

		req, err := http.NewRequest(e.Method, targetBase+parsed.RequestURI(), nil)
		if err != nil {
			return ReplayResult{}, err
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
		resp.Body.Close()

		sig := fmt.Sprintf("%s %s %d", e.Method, parsed.RequestURI(), resp.StatusCode)
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
		CompletedEvents:    len(events),
		TotalEvents:        len(events),
		LastProgressIndex:  lastProgressIndex,
		RunDuration:        time.Since(start),
		ExpectedDuration:   expected,
		ResponseSignatures: signatures,
		ErrorCount:         errCount,
	}, nil
}
