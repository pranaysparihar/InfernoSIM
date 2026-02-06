package replaydriver

import "testing"

func TestGoldenReplay(t *testing.T) {
	inbound := writeInboundLog(t, []string{
		`{"id":"1","type":"InboundRequest","timestamp":"2026-02-02T08:30:43Z","method":"GET","url":"http://localhost:18081/api/test?q=verify"}`,
	})

	events, err := LoadInboundEvents(inbound)
	if err != nil {
		t.Fatalf("load inbound failed: %v", err)
	}

	r1, err := ReplayEvents(events, "http://localhost:18080", ReplayConfig{
		TimeScale: 1.0,
		Density:   1.0,
		MinGap:    0,
	})
	if err != nil {
		t.Fatalf("first replay failed: %v", err)
	}

	r2, err := ReplayEvents(events, "http://localhost:18080", ReplayConfig{
		TimeScale: 1.0,
		Density:   1.0,
		MinGap:    0,
	})
	if err != nil {
		t.Fatalf("second replay failed: %v", err)
	}

	if r1.Fingerprint != r2.Fingerprint {
		t.Fatal("fingerprints differ")
	}
}
