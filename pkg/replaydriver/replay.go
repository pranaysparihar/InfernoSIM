package replaydriver

import (
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
	Fingerprint [32]byte
}

// loadInbound reads inbound events from inbound.log (JSON stream) and returns only InboundRequest events.
func loadInbound(inboundLog string) ([]event.Event, error) {
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
			return nil, fmt.Errorf("inbound log parse error: %w", err)
		}
		if e.Type == "InboundRequest" {
			evs = append(evs, e)
		}
	}

	return evs, nil
}

// Replay replays captured inbound requests deterministically.
//
// inboundLog MUST point to the inbound.log file.
// targetBase is the inbound proxy base URL (e.g. "http://localhost:18080").
// timeScale: 1.0 = real-time, 0.1 = 10x faster, 2.0 = 2x slower.
// maxGap caps idle time between replayed events after scaling (0 disables the cap).
func Replay(inboundLog, targetBase string, timeScale float64, maxGap time.Duration) (ReplayResult, error) {
	reqs, err := loadInbound(inboundLog)
	if err != nil {
		return ReplayResult{}, err
	}
	if len(reqs) == 0 {
		return ReplayResult{}, fmt.Errorf("no inbound requests found")
	}
	if timeScale <= 0 {
		return ReplayResult{}, fmt.Errorf("timeScale must be > 0 (got %.3f)", timeScale)
	}

	h := sha256.New()

	// Keep a finite timeout so "replay" never hangs forever on a stuck upstream.
	// (This is separate from time semantics; it's basic safety.)
	client := &http.Client{
		Timeout: 15 * time.Second,
	}

	// Safe time semantics:
	// - Preserve *per-event* gaps (not cumulative from t0)
	// - Apply timeScale
	// - Clamp with maxGap (if > 0)
	// - Enforce monotonic schedule
	prevTS := reqs[0].Timestamp
	lastScheduled := time.Now() // "now" is when replay starts

	for i, e := range reqs {
		rawGap := e.Timestamp.Sub(prevTS)
		if rawGap < 0 {
			// Log clocks can be weird; never go backwards.
			rawGap = 0
		}

		scaledGap := time.Duration(float64(rawGap) * timeScale)
		if maxGap > 0 && scaledGap > maxGap {
			scaledGap = maxGap
		}

		// Schedule relative to the last scheduled time (monotonic)
		scheduled := lastScheduled.Add(scaledGap)

		if i == 0 || i%10 == 0 {
			wait := time.Until(scheduled)
			if wait < 0 {
				wait = 0
			}
			log.Printf(
				"Replay progress: %d/%d | rawGap=%s scaledGap=%s wait=%s timeScale=%.3f maxGap=%s",
				i+1, len(reqs), rawGap, scaledGap, wait, timeScale, maxGap,
			)
		}

		if wait := time.Until(scheduled); wait > 0 {
			time.Sleep(wait)
		}

		// Move forward
		lastScheduled = scheduled
		prevTS = e.Timestamp

		// --- URL reconstruction ---
		parsed, err := urlpkg.Parse(e.URL)
		if err != nil {
			return ReplayResult{}, fmt.Errorf("bad captured URL %q: %w", e.URL, err)
		}

		replayURL := targetBase + parsed.RequestURI()

		req, err := http.NewRequest(e.Method, replayURL, nil)
		if err != nil {
			return ReplayResult{}, fmt.Errorf("replay request %d build failed: %w", i, err)
		}

		resp, err := client.Do(req)
		if err != nil {
			return ReplayResult{}, fmt.Errorf("replay request %d failed: %w", i, err)
		}
		_ = resp.Body.Close()

		// --- deterministic fingerprint (coarse but stable) ---
		h.Write([]byte(e.Method))
		h.Write([]byte(parsed.RequestURI()))
		h.Write([]byte(fmt.Sprint(resp.StatusCode)))
	}

	var sum [32]byte
	copy(sum[:], h.Sum(nil))
	return ReplayResult{Fingerprint: sum}, nil
}

func VerifyDeterministic(inboundLog, targetBase string, timeScale float64, maxGap time.Duration, runs int) ([32]byte, error) {
	var ref [32]byte

	if runs <= 0 {
		return ref, fmt.Errorf("runs must be > 0 (got %d)", runs)
	}

	for i := 0; i < runs; i++ {
		r, err := Replay(inboundLog, targetBase, timeScale, maxGap)
		if err != nil {
			return ref, err
		}
		if i == 0 {
			ref = r.Fingerprint
		} else if r.Fingerprint != ref {
			return ref, fmt.Errorf("non-deterministic replay: run %d fingerprint mismatch", i+1)
		}
	}

	return ref, nil
}
