package replaydriver

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	urlpkg "net/url"
	"os"
	"time"

	"infernosim/pkg/event"
)

type ReplayResult struct {
	Fingerprint [32]byte
}

// loadInbound reads inbound events from inbound.log (JSON stream)
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
// inboundLog MUST point to the inbound.log file.
// timeScale: 1.0 = real-time, 0.1 = 10x faster, 2.0 = 2x slower
func Replay(inboundLog string, targetBase string, timeScale float64) (ReplayResult, error) {
	reqs, err := loadInbound(inboundLog)
	if err != nil {
		return ReplayResult{}, err
	}
	if len(reqs) == 0 {
		return ReplayResult{}, fmt.Errorf("no inbound requests found")
	}

	h := sha256.New()
	client := &http.Client{}

	// scheduling baseline
	t0 := reqs[0].Timestamp
	start := time.Now()

	for i, e := range reqs {
		// --- time control ---
		dt := e.Timestamp.Sub(t0)
		sleep := time.Duration(float64(dt) * timeScale)

		deadline := start.Add(sleep)
		if deadline.After(time.Now()) {
			time.Sleep(deadline.Sub(time.Now()))
		}

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

		// --- deterministic fingerprint ---
		h.Write([]byte(e.Method))
		h.Write([]byte(parsed.RequestURI()))
		h.Write([]byte(fmt.Sprint(resp.StatusCode)))
	}

	var sum [32]byte
	copy(sum[:], h.Sum(nil))
	return ReplayResult{Fingerprint: sum}, nil
}

func VerifyDeterministic(inboundLog, targetBase string, timeScale float64, runs int) error {
	var ref [32]byte

	for i := 0; i < runs; i++ {
		r, err := Replay(inboundLog, targetBase, timeScale)
		if err != nil {
			return err
		}
		if i == 0 {
			ref = r.Fingerprint
		} else if r.Fingerprint != ref {
			return fmt.Errorf("non-deterministic replay: run %d fingerprint mismatch", i+1)
		}
	}

	return nil
}
