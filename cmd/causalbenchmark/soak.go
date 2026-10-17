package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"infernosim/pkg/agentrunner"
)

// Samples are taken between complete cycles after GC. Limits are deliberately
// fixed before running the campaign, and are growth limits over a warm baseline.
type resources struct {
	Cycle      int    `json:"cycle"`
	HeapBytes  uint64 `json:"heap_bytes"`
	RSSBytes   uint64 `json:"rss_bytes"`
	Goroutines int    `json:"goroutines"`
	FDs        int    `json:"file_descriptors"`
	Children   int    `json:"remaining_descendants"`
	Orphans    int    `json:"orphaned_fixture_processes"`
}
type soakReport struct {
	Platform     string        `json:"platform"`
	Started      time.Time     `json:"started"`
	Duration     time.Duration `json:"duration"`
	Required     time.Duration `json:"required_duration"`
	BinarySHA256 string        `json:"binary_sha256"`
	Cycles       int           `json:"cycles"`
	Runs         int           `json:"runs"`
	Passed       bool          `json:"passed"`
	Failure      string        `json:"failure,omitempty"`
	Baseline     resources     `json:"baseline"`
	Peaks        resources     `json:"peaks"`
	Samples      []resources   `json:"samples"`
	Limits       resources     `json:"growth_limits"`
}

func sample(cycle int, app, binary string) (resources, error) {
	runtime.GC()
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	r := resources{Cycle: cycle, HeapBytes: mem.HeapAlloc, Goroutines: runtime.NumGoroutine()}
	pid := strconv.Itoa(os.Getpid())
	b, err := exec.Command("ps", "-p", pid, "-o", "rss=").Output()
	if err != nil {
		return r, err
	}
	kib, err := strconv.ParseUint(strings.TrimSpace(string(b)), 10, 64)
	if err != nil {
		return r, err
	}
	r.RSSBytes = kib * 1024
	if runtime.GOOS == "linux" {
		fds, err := os.ReadDir("/proc/self/fd")
		if err != nil {
			return r, err
		}
		r.FDs = len(fds)
	} else {
		b, err := exec.Command("lsof", "-a", "-p", pid, "-Ff").Output()
		if err != nil {
			return r, err
		}
		for _, line := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(line, "f") {
				if _, err := strconv.Atoi(line[1:]); err == nil {
					r.FDs++
				}
			}
		}
	}
	cmd := exec.Command("ps", "-axo", "pid=,ppid=,command=")
	b, err = cmd.Output()
	if err != nil {
		return r, err
	}
	parents := map[int]int{}
	for _, line := range strings.Split(string(b), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		child, _ := strconv.Atoi(fields[0])
		parent, _ := strconv.Atoi(fields[1])
		if parent == 1 && (strings.Contains(line, " "+app+" ") || strings.Contains(line, binary+" agent mcp connect")) {
			r.Orphans++
		}
		if child != cmd.Process.Pid {
			parents[child] = parent
		}
	}
	for child := range parents {
		seen := map[int]bool{}
		for p := child; p > 1 && !seen[p]; p = parents[p] {
			seen[p] = true
			if parents[p] == os.Getpid() {
				r.Children++
				break
			}
		}
	}
	return r, nil
}

func runSoak(binary, python, app, input, out string, duration time.Duration) (resultErr error) {
	if binary == "" || input == "" || duration <= 0 || (runtime.GOOS != "darwin" && runtime.GOOS != "linux") {
		return fmt.Errorf("soak requires -binary, -reproductions and Linux/macOS resource instrumentation")
	}
	binary, _ = filepath.Abs(binary)
	app, _ = filepath.Abs(app)
	input, _ = filepath.Abs(input)
	out, _ = filepath.Abs(out)
	if err := os.Mkdir(out, 0700); err != nil {
		return err
	}
	bytes, err := os.ReadFile(binary)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(bytes)
	r := soakReport{Platform: runtime.GOOS + "/" + runtime.GOARCH, Started: time.Now().UTC(), Required: duration,
		BinarySHA256: hex.EncodeToString(sum[:]), Limits: resources{HeapBytes: 32 << 20, RSSBytes: 96 << 20, Goroutines: 8, FDs: 8}}
	write := func() error {
		r.Duration = time.Since(r.Started)
		return agentrunner.WriteJSON(filepath.Join(out, "soak.json"), r)
	}
	defer func() {
		if resultErr != nil {
			r.Failure = resultErr.Error()
		}
		if err := write(); resultErr == nil {
			resultErr = err
		}
	}()
	data, err := os.ReadFile(filepath.Join(input, "acceptance.json"))
	if err != nil {
		return err
	}
	var acceptance report
	if err := json.Unmarshal(data, &acceptance); err != nil {
		return err
	}
	if !acceptance.Passed {
		return fmt.Errorf("soak requires a passing acceptance campaign")
	}
	// Relocate every selected artifact, so no run can quietly use its original incident.
	type fixture struct {
		scene    string
		manifest agentrunner.Reproduction
		options  agentrunner.Options
	}
	var fixtures []fixture
	seen := map[string]bool{}
	for _, entry := range acceptance.Scenes {
		if seen[entry.Scenario] {
			continue
		}
		seen[entry.Scenario] = true
		source := filepath.Join(input, filepath.Base(entry.Reproduction))
		manifest, err := agentrunner.LoadReproduction(source)
		if err != nil {
			return err
		}
		dest := filepath.Join(out, entry.Scenario+"-relocated")
		if err := agentrunner.ExportReproduction(dest, filepath.Join(source, "incident"), "", manifest); err != nil {
			return err
		}
		manifest, err = agentrunner.LoadReproduction(dest)
		if err != nil {
			return err
		}
		incident := filepath.Join(dest, "incident")
		prepared, err := agentrunner.Prepare(incident, "", 7100, 1, true)
		if err != nil {
			return err
		}
		base := agentrunner.Options{IncidentDir: incident, ConfigPath: prepared.ConfigPath, Timeout: manifest.Timeout,
			RestartAfterCall: manifest.RestartAfterCall, MCPConnector: []string{binary, "agent", "mcp", "connect"},
			StateCheck: agentrunner.StateCheckOptions{SetupCommand: []string{python, app, "--scenario", entry.Scenario, "--setup"},
				Command: []string{python, app, "--scenario", entry.Scenario, "--check"}, IDs: manifest.CheckIDs, Timeout: manifest.CheckTimeout, Expected: manifest.Checker, Legacy: manifest.Checker == nil}}
		opts, err := agentrunner.RunTrial(base, prepared.Config.Agent, manifest.Trial)
		if err != nil {
			return err
		}
		fixtures = append(fixtures, fixture{entry.Scenario, manifest, opts})
	}
	if len(fixtures) != 5 {
		return fmt.Errorf("soak requires all five application scenarios")
	}
	ctx := context.Background()
	for time.Since(r.Started) < duration || r.Cycles < 4 {
		for _, f := range fixtures {
			for _, mode := range []string{"safe", "unsafe"} {
				opts := f.options
				opts.Command = []string{python, app, "--scenario", f.scene, "--mode", mode}
				result, err := agentrunner.Run(ctx, opts)
				if err != nil {
					return err
				}
				r.Runs++
				valid := result.CausalExecution != nil && result.CausalExecution.Validate(f.manifest.Trial.Causal.Graph, f.manifest.Trial.Causal.Schedule) == nil
				namedFailure := false
				for _, assertion := range result.Assertions {
					if assertion.ID == f.manifest.Assertion && !assertion.Passed {
						namedFailure = true
					}
				}
				if mode == "unsafe" && !namedFailure {
					valid = false
				}
				if !valid || result.InvalidRun || result.Process.ExitCode != 0 || result.Process.TimedOut || result.Passed != (mode == "safe") {
					_ = agentrunner.WriteJSON(filepath.Join(out, "failed-run.json"), result)
					return fmt.Errorf("cycle %d %s %s nondeterministic/invalid result: %s", r.Cycles, f.scene, mode, result.Failure)
				}
			}
		}
		r.Cycles++
		if r.Cycles < 3 || (r.Cycles != 3 && r.Cycles%10 != 0) {
			continue
		}
		s, err := sample(r.Cycles, app, binary)
		if err != nil {
			return err
		}
		if r.Cycles == 3 {
			r.Baseline = s
		}
		r.Peaks.HeapBytes = max(r.Peaks.HeapBytes, s.HeapBytes)
		r.Peaks.RSSBytes = max(r.Peaks.RSSBytes, s.RSSBytes)
		r.Peaks.Goroutines = max(r.Peaks.Goroutines, s.Goroutines)
		r.Peaks.FDs = max(r.Peaks.FDs, s.FDs)
		r.Peaks.Children = max(r.Peaks.Children, s.Children)
		r.Peaks.Orphans = max(r.Peaks.Orphans, s.Orphans)
		r.Samples = append(r.Samples, s)
		if s.Children != 0 || s.Orphans != 0 || s.HeapBytes > r.Baseline.HeapBytes+r.Limits.HeapBytes || s.RSSBytes > r.Baseline.RSSBytes+r.Limits.RSSBytes || s.Goroutines > r.Baseline.Goroutines+r.Limits.Goroutines || s.FDs > r.Baseline.FDs+r.Limits.FDs {
			return fmt.Errorf("soak resource growth exceeded predeclared limits: %+v", s)
		}
		if err := write(); err != nil {
			return err
		}
		fmt.Printf("soak cycles=%d runs=%d elapsed=%s heap=%d rss=%d goroutines=%d fds=%d children=%d\n", r.Cycles, r.Runs, time.Since(r.Started).Round(time.Second), s.HeapBytes, s.RSSBytes, s.Goroutines, s.FDs, s.Children)
	}
	r.Passed = true
	return nil
}
