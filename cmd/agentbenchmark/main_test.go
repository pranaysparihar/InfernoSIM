package main

import (
	"testing"
	"time"
)

func TestPercentileAndMilliseconds(t *testing.T) {
	if got := milliseconds(1500 * time.Microsecond); got != 1.5 {
		t.Fatalf("milliseconds = %v", got)
	}
	if got := percentile([]float64{4, 1, 3, 2}, 0.50); got != 3 {
		t.Fatalf("p50 = %v", got)
	}
	if got := percentile(nil, 0.95); got != 0 {
		t.Fatalf("empty percentile = %v", got)
	}
}
