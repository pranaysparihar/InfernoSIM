package replaydriver

import (
	"os"
	"testing"
)

func writeInboundLog(t *testing.T, lines []string) string {
	t.Helper()

	f, err := os.CreateTemp("", "inbound-*.log")
	if err != nil {
		t.Fatalf("temp file failed: %v", err)
	}

	for _, l := range lines {
		_, _ = f.WriteString(l + "\n")
	}

	_ = f.Close()
	return f.Name()
}
