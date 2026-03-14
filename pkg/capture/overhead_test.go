package capture

import (
	"testing"
	"time"
)

func TestCaptureOverhead(t *testing.T) {
	const N = 1000

	noCapture := benchmark(N, false)
	withCapture := benchmark(N, true)

	overhead := float64(withCapture-noCapture) / float64(noCapture)

	if overhead > 0.05 {
		t.Fatalf("overhead too high: %.2f%%", overhead*100)
	}
}
func benchmark(n int, capture bool) time.Duration {
	start := time.Now()
	for i := 0; i < n; i++ {
		// hit handler
	}
	return time.Since(start)
}
