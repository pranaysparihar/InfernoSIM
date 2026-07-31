package event

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
)

func TestGenerateAndSanitizeTraceID(t *testing.T) {
	id := GenerateID()
	if len(id) != 32 || SanitizeTraceID(id) != id {
		t.Fatalf("invalid generated ID %q", id)
	}
	for _, invalid := range []string{"", "ABC", id + "\n", "0000000000000000000000000000000g"} {
		if got := SanitizeTraceID(invalid); got != "" {
			t.Errorf("SanitizeTraceID(%q) = %q", invalid, got)
		}
	}
}

func TestLoggerConcurrentJSONLAndPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.log")
	logger, err := NewLogger(path)
	if err != nil {
		t.Fatal(err)
	}
	const count = 50
	var wg sync.WaitGroup
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := logger.Write(&Event{ID: GenerateID(), Type: "test"}); err != nil {
				t.Errorf("Write: %v", err)
			}
		}()
	}
	wg.Wait()
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("permissions = %o", info.Mode().Perm())
	}

	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	seenSequences := make(map[int64]bool)
	dec := json.NewDecoder(f)
	for i := 0; i < count; i++ {
		var evt Event
		if err := dec.Decode(&evt); err != nil {
			t.Fatal(err)
		}
		seenSequences[evt.Sequence] = true
	}
	if len(seenSequences) != count {
		t.Fatalf("unique sequences = %d", len(seenSequences))
	}
}

func TestLoggerContinuesSequenceAndRepairsPartialTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.log")
	if err := os.WriteFile(path, []byte(
		"{\"id\":\"first\",\"type\":\"test\",\"timestamp\":\"0001-01-01T00:00:00Z\",\"sequence\":7}\n{\"partial\":",
	), 0o600); err != nil {
		t.Fatal(err)
	}
	logger, err := NewLogger(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := logger.Write(&Event{ID: "second", Type: "test"}); err != nil {
		t.Fatal(err)
	}
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}

	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	dec := json.NewDecoder(f)
	var first, second Event
	if err := dec.Decode(&first); err != nil {
		t.Fatal(err)
	}
	if err := dec.Decode(&second); err != nil {
		t.Fatal(err)
	}
	if second.Sequence != 8 {
		t.Fatalf("continued sequence = %d", second.Sequence)
	}
}
