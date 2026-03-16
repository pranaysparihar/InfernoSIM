package replaydriver

import (
	"encoding/json"
	"fmt"
	"os"
)

// StateAdapter allows external systems to seed the RuntimeState before replay.
// Implement this interface to load pre-existing value mappings from Redis,
// a database, a feature flag system, or a local file.
type StateAdapter interface {
	// Load returns a map of old_value -> new_value pairs to pre-populate state.
	Load() (map[string]string, error)
	// Name returns a human-readable identifier for logging.
	Name() string
}

// FileStateAdapter reads a JSON object of {"old_value": "new_value"} pairs
// from a local file. Useful for injecting known token substitutions before replay.
type FileStateAdapter struct {
	Path string
}

func (f *FileStateAdapter) Load() (map[string]string, error) {
	data, err := os.ReadFile(f.Path)
	if err != nil {
		return nil, fmt.Errorf("FileStateAdapter: read %s: %w", f.Path, err)
	}
	var m map[string]string
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("FileStateAdapter: parse %s: %w", f.Path, err)
	}
	return m, nil
}

func (f *FileStateAdapter) Name() string { return "file:" + f.Path }

// ApplyAdapters loads state from each adapter and populates rs.
// Errors from individual adapters are collected and returned together.
func ApplyAdapters(rs *RuntimeState, adapters []StateAdapter) error {
	var errs []error
	for _, a := range adapters {
		vals, err := a.Load()
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", a.Name(), err))
			continue
		}
		for old, newVal := range vals {
			rs.Put(old, newVal)
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("state adapter errors: %v", errs)
	}
	return nil
}
