package inject

import (
	"sync"
	"testing"
)

func TestParseConfigRejectsInvalidValues(t *testing.T) {
	for _, raw := range []string{
		"drop=101%",
		"reset=-1%",
		"status=999,rate=100%",
		"unknown=value",
		"malformed",
	} {
		if _, err := ParseConfig(raw, 1); err == nil {
			t.Errorf("ParseConfig(%q) unexpectedly succeeded", raw)
		}
	}
}

func TestEvaluateIsDeterministicForSeed(t *testing.T) {
	first, err := ParseConfig("jitter=20ms,drop=25%,reset=10%,status=503,rate=50%", 42)
	if err != nil {
		t.Fatal(err)
	}
	second, _ := ParseConfig("jitter=20ms,drop=25%,reset=10%,status=503,rate=50%", 42)
	for i := 0; i < 100; i++ {
		if a, b := first.Evaluate(true), second.Evaluate(true); a != b {
			t.Fatalf("action %d differs: %+v != %+v", i, a, b)
		}
	}
}

func TestEvaluateConcurrent(t *testing.T) {
	cfg, err := ParseConfig("jitter=5ms,drop=5%,reset=5%,status=503,rate=10%", 7)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = cfg.Evaluate(true)
		}()
	}
	wg.Wait()
}
