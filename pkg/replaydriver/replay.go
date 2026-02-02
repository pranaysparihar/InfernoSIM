package replaydriver

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	urlpkg "net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"infernosim/pkg/event"
)

type ReplayResult struct {
	Fingerprint [32]byte
}

func normalizeIncidentDir(p string) string {
	// If user passed .../events.log, use its parent
	if strings.HasSuffix(p, "/events.log") {
		return filepath.Dir(p)
	}

	// If it's an existing file, use parent dir
	if st, err := os.Stat(p); err == nil && !st.IsDir() {
		return filepath.Dir(p)
	}

	// Otherwise assume directory
	return p
}

// loadInbound reads inbound events from <incident>/events.log
func loadInbound(incidentDir string) ([]event.Event, error) {
	logPath := filepath.Join(incidentDir, "events.log")

	f, err := os.Open(logPath)
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
// incidentDir MUST be a directory containing events.log
// timeScale: 1.0 = real-time, 0.1 = 10x faster
func Replay(incidentDir string, targetBase string, timeScale float64) (ReplayResult, error) {
	incidentDir = normalizeIncidentDir(incidentDir)
	reqs, err := loadInbound(incidentDir)
	if err != nil {
		return ReplayResult{}, err
	}
	if len(reqs) == 0 {
		return ReplayResult{}, fmt.Errorf("no inbound requests found")
	}

	h := sha256.New()
	client := &http.Client{}

	// Logical scheduling baseline
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
		resp.Body.Close()

		// --- deterministic fingerprint ---
		h.Write([]byte(e.Method))
		h.Write([]byte(parsed.RequestURI()))
		h.Write([]byte(fmt.Sprint(resp.StatusCode)))
	}

	var sum [32]byte
	copy(sum[:], h.Sum(nil))
	return ReplayResult{Fingerprint: sum}, nil
}

func VerifyDeterministic(incidentDir, targetBase string, timeScale float64, runs int) error {
	var ref [32]byte

	for i := 0; i < runs; i++ {
		r, err := Replay(incidentDir, targetBase, timeScale)
		if err != nil {
			return err
		}
		if i == 0 {
			ref = r.Fingerprint
		} else if r.Fingerprint != ref {
			return fmt.Errorf(
				"non-deterministic replay: run %d fingerprint mismatch",
				i+1,
			)
		}
	}

	return nil
}
