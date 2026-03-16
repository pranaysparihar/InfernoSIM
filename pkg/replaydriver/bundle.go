package replaydriver

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// IncidentBundle describes the standard incident directory layout:
//
//	incident/
//	 ├─ incident.json   ← metadata
//	 ├─ inbound.log     ← JSONL of InboundRequest events (required)
//	 ├─ outbound.log    ← JSONL of outbound dependency events (optional)
//	 └─ replay.yaml     ← config-driven replay settings (optional)
type IncidentBundle struct {
	Dir          string
	MetadataPath string // incident.json
	InboundLog   string // inbound.log (required)
	OutboundLog  string // outbound.log (optional, may not exist)
	ConfigPath   string // replay.yaml (optional, may not exist)
}

// IncidentMetadata is written to incident.json by infernosim record.
type IncidentMetadata struct {
	CapturedAt    time.Time `json:"captured_at"`
	Env           string    `json:"env,omitempty"`
	Host          string    `json:"host,omitempty"`
	Listen        string    `json:"listen,omitempty"`
	Forward       string    `json:"forward,omitempty"`
	InboundCount  int       `json:"inbound_count"`
	OutboundCount int       `json:"outbound_count"`
}

// OpenBundle resolves the standard file paths within dir.
// Returns an error if inbound.log is missing.
func OpenBundle(dir string) (IncidentBundle, error) {
	inbound := filepath.Join(dir, "inbound.log")
	if _, err := os.Stat(inbound); err != nil {
		return IncidentBundle{}, fmt.Errorf("incident bundle at %q is missing inbound.log: %w", dir, err)
	}

	b := IncidentBundle{
		Dir:          dir,
		MetadataPath: filepath.Join(dir, "incident.json"),
		InboundLog:   inbound,
		OutboundLog:  filepath.Join(dir, "outbound.log"),
		ConfigPath:   filepath.Join(dir, "replay.yaml"),
	}
	return b, nil
}

// HasOutbound reports whether outbound.log exists in the bundle.
func (b IncidentBundle) HasOutbound() bool {
	_, err := os.Stat(b.OutboundLog)
	return err == nil
}

// HasConfig reports whether replay.yaml exists in the bundle.
func (b IncidentBundle) HasConfig() bool {
	_, err := os.Stat(b.ConfigPath)
	return err == nil
}

// ReadMetadata parses incident.json. Returns zero value if the file doesn't exist.
func (b IncidentBundle) ReadMetadata() (IncidentMetadata, error) {
	data, err := os.ReadFile(b.MetadataPath)
	if err != nil {
		if os.IsNotExist(err) {
			return IncidentMetadata{}, nil
		}
		return IncidentMetadata{}, err
	}
	var m IncidentMetadata
	if err := json.Unmarshal(data, &m); err != nil {
		return IncidentMetadata{}, fmt.Errorf("malformed incident.json: %w", err)
	}
	return m, nil
}

// WriteMetadata writes m to incident.json, creating the directory if needed.
func WriteMetadata(dir string, m IncidentMetadata) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "incident.json"), data, 0o644)
}
