package replaydriver

import (
	"fmt"
	"infernosim/pkg/event"
	"time"
)

type EventDiff struct {
	Index       int
	Captured    event.Event
	Replayed    event.Event
	StatusDiff   string
	StatusChange string
	HeaderDiff   []string
	BodyDiff    string
	LatencyDiff string
}

func CompareEvents(captured, replayed event.Event, index int) *EventDiff {
	diff := &EventDiff{
		Index:    index,
		Captured: captured,
		Replayed: replayed,
	}

	hasDiff := false

	// 1. Status Diff
	if captured.Status != replayed.Status {
		diff.StatusChange = fmt.Sprintf("%d -> %d", captured.Status, replayed.Status)
		diff.StatusDiff = fmt.Sprintf("Expected %d, Got %d", captured.Status, replayed.Status)
		hasDiff = true
	}

	// 2. Latency Diff (heuristic: > 20% deviation and > 50ms)
	recordedLat := captured.Duration
	actualLat := replayed.Duration
	latDelta := actualLat - recordedLat
	if latDelta < 0 {
		latDelta = -latDelta
	}
	if latDelta > 50*time.Millisecond && float64(latDelta) > 0.2*float64(recordedLat) {
		diff.LatencyDiff = fmt.Sprintf("Expected %s, Got %s (delta %s)", recordedLat, actualLat, latDelta)
		hasDiff = true
	}

	// 3. Header check — compare all headers present in the captured event
	for h, cv := range captured.Headers {
		rv := replayed.Headers[h]
		if len(cv) > 0 && (len(rv) == 0 || cv[0] != rv[0]) {
			diff.HeaderDiff = append(diff.HeaderDiff, fmt.Sprintf("Header %s: Expected %v, Got %v", h, cv, rv))
			hasDiff = true
		}
	}

	if !hasDiff {
		return nil
	}
	return diff
}

func PrintDiffs(diffs []*EventDiff) {
	if len(diffs) == 0 {
		fmt.Println("No significant differences found in replay.")
		return
	}

	fmt.Println("\n=== REPLAY DIFF ANALYSIS ===")
	for _, d := range diffs {
		fmt.Printf("\nEvent #%d: %s %s\n", d.Index, d.Captured.Method, d.Captured.URL)
		if d.StatusDiff != "" {
			fmt.Printf("  [STATUS]  %s\n", d.StatusDiff)
		}
		if d.LatencyDiff != "" {
			fmt.Printf("  [LATENCY] %s\n", d.LatencyDiff)
		}
		for _, hd := range d.HeaderDiff {
			fmt.Printf("  [HEADER]  %s\n", hd)
		}
	}
	fmt.Println("============================")
}
