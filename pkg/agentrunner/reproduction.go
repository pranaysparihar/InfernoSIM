package agentrunner

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
	"infernosim/pkg/replaydriver"
	"infernosim/pkg/reporting"
)

const reproductionLimit = 64 << 20

// Reproduction is inert data. Command argv is deliberately not stored;
// the caller supplies application/setup/check argv again when reproducing.
type Reproduction struct {
	Version          int               `json:"version"`
	Trial            Trial             `json:"trial"`
	Assertion        string            `json:"assertion,omitempty"`
	CheckIDs         []string          `json:"check_ids,omitempty"`
	RequiresSetup    bool              `json:"requires_setup"`
	Timeout          time.Duration     `json:"timeout"`
	CheckTimeout     time.Duration     `json:"check_timeout"`
	RestartAfterCall int               `json:"restart_after_call,omitempty"`
	Files            map[string]string `json:"files"`
}

func inside(root, path string) (string, error) {
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("reproduction dependency must be inside incident directory")
	}
	return rel, nil
}
func readRegular(path string, limit int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > limit {
		return nil, fmt.Errorf("reproduction requires bounded regular files")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("reproduction file limit exceeded")
	}
	return data, nil
}
func digest(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }
func ExportReproduction(destination, incident, configPath string, r Reproduction) (err error) {
	root, err := filepath.Abs(incident)
	if err != nil {
		return err
	}
	dest, err := filepath.Abs(destination)
	if err != nil {
		return err
	}
	if _, err := inside(root, dest); err == nil {
		return fmt.Errorf("reproduction output must be outside source incident")
	}
	if info, err := os.Lstat(root); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("incident must be a real directory")
	}
	if configPath == "" {
		configPath = filepath.Join(root, "replay.yaml")
	}
	cfg, err := replaydriver.LoadReplayConfig(configPath)
	if err != nil {
		return err
	}
	// Relocate only declared schema files within the incident. Never silently
	// create an artifact which depends on the exporting machine's absolute paths.
	for _, paths := range []*[]string{&cfg.Matching.GRPC.ProtoFiles, &cfg.Matching.GRPC.DescriptorSets, &cfg.Matching.GRPC.ImportPaths} {
		for i, p := range *paths {
			p, err = filepath.Abs(p)
			if err != nil {
				return err
			}
			rel, e := inside(root, p)
			if e != nil {
				return e
			}
			(*paths)[i] = rel
		}
	}
	if cfg.Stub.HTTPS.CADir != "" || cfg.State.File != "" {
		return fmt.Errorf("portable reproduction requires no external CA/state file; keep test state in explicit hooks")
	}
	if _, err := r.Trial.AgentConfig(cfg.Agent); err != nil {
		return err
	}
	configBytes, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return err
	}
	if err = os.Mkdir(dest, 0o700); err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = os.RemoveAll(dest)
		}
	}()
	r.Version = 1
	r.Files = map[string]string{}
	var total int64
	write := func(name string, data []byte) error {
		total += int64(len(data))
		if total > reproductionLimit || len(r.Files) >= 1000 {
			return fmt.Errorf("reproduction exceeds 64 MiB or 1000 files")
		}
		if err := reporting.WritePrivateFile(filepath.Join(dest, filepath.FromSlash(name)), data); err != nil {
			return err
		}
		r.Files[name] = digest(data)
		return nil
	}
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("reproduction refuses symlinks")
		}
		if entry.IsDir() {
			return nil
		}
		rel, e := filepath.Rel(root, path)
		if e != nil {
			return e
		}
		if rel == "replay.yaml" {
			return nil
		}
		data, e := readRegular(path, reproductionLimit-total)
		if e != nil {
			return e
		}
		return write("incident/"+filepath.ToSlash(rel), data)
	})
	if err != nil {
		return err
	}
	if err = write("incident/replay.yaml", configBytes); err != nil {
		return err
	}
	if _, err = replaydriver.LoadReplayConfig(filepath.Join(dest, "incident", "replay.yaml")); err != nil {
		return err
	}
	return WriteJSON(filepath.Join(dest, "reproduction.json"), r)
}
func LoadReproduction(dir string) (Reproduction, error) {
	var r Reproduction
	root, err := filepath.Abs(dir)
	if err != nil {
		return r, err
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return r, fmt.Errorf("reproduction must be a real directory")
	}
	data, err := readRegular(filepath.Join(root, "reproduction.json"), 1<<20)
	if err != nil {
		return r, err
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err = d.Decode(&r); err != nil {
		return r, err
	}
	if d.Decode(new(any)) != io.EOF {
		return r, fmt.Errorf("unexpected manifest data")
	}
	if r.Version != 1 || len(r.Files) == 0 || len(r.Files) > 1000 || r.Files["incident/replay.yaml"] == "" {
		return r, fmt.Errorf("invalid reproduction manifest")
	}
	total := int64(0)
	seen := map[string]bool{}
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("reproduction refuses symlinks")
		}
		if entry.IsDir() {
			return nil
		}
		rel, e := filepath.Rel(root, path)
		if e != nil {
			return e
		}
		name := filepath.ToSlash(rel)
		if name == "reproduction.json" {
			return nil
		}
		if r.Files[name] == "" {
			return fmt.Errorf("unmanifested reproduction file")
		}
		b, e := readRegular(path, reproductionLimit-total)
		if e != nil {
			return e
		}
		total += int64(len(b))
		if total > reproductionLimit {
			return fmt.Errorf("reproduction exceeds size limit")
		}
		if digest(b) != r.Files[name] {
			return fmt.Errorf("reproduction checksum mismatch: %s", name)
		}
		seen[name] = true
		return nil
	})
	if err != nil {
		return r, err
	}
	if len(seen) != len(r.Files) {
		return r, fmt.Errorf("missing reproduction files")
	}
	if r.Timeout < time.Millisecond || r.Timeout > MaximumTimeout || r.CheckTimeout < time.Millisecond || r.CheckTimeout > time.Minute {
		return r, fmt.Errorf("invalid reproduction timeout")
	}
	configData, err := readRegular(filepath.Join(root, "incident", "replay.yaml"), reproductionLimit)
	if err != nil {
		return r, err
	}
	var cfg replaydriver.ReplayYAMLConfig
	if err := yaml.Unmarshal(configData, &cfg); err != nil {
		return r, err
	}
	if cfg.Stub.HTTPS.CADir != "" || cfg.State.File != "" {
		return r, fmt.Errorf("reproduction contains an external CA/state dependency")
	}
	for _, paths := range [][]string{cfg.Matching.GRPC.ProtoFiles, cfg.Matching.GRPC.DescriptorSets, cfg.Matching.GRPC.ImportPaths} {
		for _, path := range paths {
			if filepath.IsAbs(path) {
				return r, fmt.Errorf("absolute reproduction dependency")
			}
			if _, err := inside(filepath.Join(root, "incident"), filepath.Join(root, "incident", path)); err != nil {
				return r, err
			}
		}
	}
	return r, nil
}
