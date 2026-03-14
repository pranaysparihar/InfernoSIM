package replaydriver

import (
	"testing"
	"time"
)

func TestReplayTimeExpanded(t *testing.T) {
	inbound := writeInboundLog(t, []string{
		`{"id":"1","type":"InboundRequest","timestamp":"2026-02-02T08:30:43.000Z","method":"GET","url":"http://localhost:18081/api/test?q=verify"}`,
		`{"id":"2","type":"InboundRequest","timestamp":"2026-02-02T08:30:43.050Z","method":"GET","url":"http://localhost:18081/api/test?q=verify2"}`,
	})

	events, err := LoadInboundEvents(inbound)
	if err != nil {
		t.Fatalf("load inbound failed: %v", err)
	}

	r, err := ReplayEvents(events, "http://localhost:9999", ReplayConfig{
		TimeScale:    1.0,
		Density:      1.0,
		MinGap:       0,
		MaxWallClock: 5 * time.Millisecond,
		MaxIdleTime:  0,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !r.TimeExpanded {
		t.Fatal("expected TimeExpanded true")
	}
}
