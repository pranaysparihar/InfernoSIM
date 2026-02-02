package replaydriver

import "testing"

func TestDivergenceDetected(t *testing.T) {
	inbound := writeInboundLog(t, []string{
		`{"id":"1","type":"InboundRequest","timestamp":"2026-02-02T08:30:43Z","method":"GET","url":"http://localhost:18081/api/test?q=verify"}`,
	})

	_, err := Replay(inbound, "http://localhost:9999", 1.0) // bad target => should fail
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}
