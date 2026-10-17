// communitybenchmark verifies the three bounded destination/recovery contracts
// requested in issues #16, #19 and #20. Its provider and application are synthetic.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"infernosim/pkg/agentrunner"
	"infernosim/pkg/causal"
)

type scene struct{ Scenario, Variant string }
type outcome struct {
	Scenario                                                          string `json:"scenario"`
	Variant                                                           string `json:"variant"`
	Seed                                                              int64  `json:"seed"`
	Schedules, Cases, SafePassed, UnsafeFailed, UnsafePassed, Invalid int
	Truncated                                                         bool   `json:"truncated"`
	Reproduction                                                      string `json:"reproduction"`
	Minimized                                                         bool   `json:"minimized"`
}
type report struct {
	Version  int           `json:"version"`
	Platform string        `json:"platform"`
	Started  time.Time     `json:"started"`
	Duration time.Duration `json:"duration"`
	Passed   bool          `json:"passed"`
	Scenes   []outcome     `json:"scenes"`
}

func main() {
	binary := flag.String("binary", "", "Built CLI for stdio transport")
	python := flag.String("python", "python3", "Python with SQLite")
	root := flag.String("fixture", "examples/recoverylab", "Fixture directory")
	out := flag.String("out", "community-acceptance", "New private evidence directory")
	iterations := flag.Int("iterations", 1, "Seeds (1..10)")
	schedules := flag.Int("schedule-budget", 12, "Bounded retry/cancel schedules per seed (1..100)")
	only := flag.String("scenario", "", "Optional scenario filter")
	flag.Parse()
	if err := run(*binary, *python, *root, *out, *iterations, *schedules, *only); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(binary, python, root, out string, iterations, schedules int, only string) error {
	if binary == "" || iterations < 1 || iterations > 10 || schedules < 1 || schedules > 100 {
		return fmt.Errorf("requires binary, iterations 1..10 and schedule-budget 1..100")
	}
	var err error
	binary, err = filepath.Abs(binary)
	if err != nil {
		return err
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return err
	}
	out, err = filepath.Abs(out)
	if err != nil {
		return err
	}
	if err = os.Mkdir(out, 0700); err != nil {
		return err
	}
	r := report{Version: 1, Platform: runtime.GOOS + "/" + runtime.GOARCH, Started: time.Now().UTC()}
	write := func() error {
		r.Duration = time.Since(r.Started)
		return agentrunner.WriteJSON(filepath.Join(out, "acceptance.json"), r)
	}
	defer write()
	scenes := []scene{{"commit-cancel", "default"}}
	for _, v := range []string{"pending", "failed-read", "stale-read", "same-key", "expired-key"} {
		scenes = append(scenes, scene{"pending", v})
	}
	for _, v := range []string{"identical-other", "delayed-visibility", "deleted", "failed-read", "expired-key", "wrong-receipt", "recovered-identity", "active-dedupe", "revoked"} {
		scenes = append(scenes, scene{"lost-handle", v})
	}
	for iteration := 0; iteration < iterations; iteration++ {
		for _, s := range scenes {
			if only != "" && only != s.Scenario {
				continue
			}
			seed := int64(42100 + iteration)
			name := fmt.Sprintf("%s-%s-%d", s.Scenario, s.Variant, seed)
			incident := filepath.Join(out, name+"-incident")
			argv := []string{filepath.Join(root, "app.py"), "--scenario", s.Scenario, "--variant", s.Variant}
			init := exec.Command(python, append(append([]string(nil), argv...), "--init", incident)...)
			if b, e := init.CombinedOutput(); e != nil {
				return fmt.Errorf("fixture init: %w: %s", e, b)
			}
			b, e := os.ReadFile(filepath.Join(incident, "declarations.json"))
			if e != nil {
				return e
			}
			var d causal.Declarations
			if e = json.Unmarshal(b, &d); e != nil {
				return e
			}
			g, e := agentrunner.DiscoverCausal(incident, d)
			if e != nil {
				return e
			}
			if e = g.Validate(true); e != nil {
				return e
			}
			p, e := agentrunner.Prepare(incident, "", seed, 100, true)
			if e != nil {
				return e
			}
			plan, e := agentrunner.PlanExploration(p.Config.Agent, p.ScopeHash, agentrunner.ExploreOptions{Seed: seed, Budget: 4 * schedules, MaxFaults: 2, CausalGraph: &g, ScheduleBudget: schedules, BoundaryTimeout: "8s"})
			if e != nil {
				return e
			}
			if e = agentrunner.WriteJSON(filepath.Join(out, name+"-plan.json"), plan); e != nil {
				return e
			}
			base := agentrunner.Options{IncidentDir: incident, ConfigPath: p.ConfigPath, Timeout: 30 * time.Second, MCPConnector: []string{binary, "agent", "mcp", "connect"}, StateCheck: agentrunner.StateCheckOptions{SetupCommand: append([]string{python}, append(append([]string(nil), argv...), "--setup")...), Command: []string{python, filepath.Join(root, "checker.py"), "--scenario", s.Scenario, "--variant", s.Variant}, IDs: []string{"destination", "authorization", "knowledge"}, Timeout: 5 * time.Second, Manifest: filepath.Join(root, "checker.json")}}
			if s.Scenario != "commit-cancel" {
				base.RestartAfterCall = 1
			}
			execute := func(trial agentrunner.Trial, mode string, options agentrunner.Options) (agentrunner.Result, error) {
				options.Command = append([]string{python}, append(append([]string(nil), argv...), "--mode", mode)...)
				options, e := agentrunner.RunTrial(options, p.Config.Agent, trial)
				if e != nil {
					return agentrunner.Result{}, e
				}
				return agentrunner.Run(context.Background(), options)
			}
			entry := outcome{Scenario: s.Scenario, Variant: s.Variant, Seed: seed, Schedules: len(plan.CausalPlan.Schedules), Cases: len(plan.Trials), Truncated: plan.Truncated}
			var retained *agentrunner.Trial
			var fingerprint *agentrunner.CheckerFingerprint
			for n, trial := range plan.Trials {
				for _, mode := range []string{"safe", "unsafe"} {
					result, e := execute(trial, mode, base)
					if e != nil {
						return e
					}
					if e = agentrunner.WriteJSON(filepath.Join(out, fmt.Sprintf("%s-%03d-%s.json", name, n, mode)), result); e != nil {
						return e
					}
					if result.InvalidRun || result.Process.ExitCode != 0 || result.Process.TimedOut {
						entry.Invalid++
						r.Scenes = append(r.Scenes, entry)
						return fmt.Errorf("%s %s invalid: %s; %s", name, mode, result.Failure, result.Process.Stderr)
					}
					if mode == "safe" {
						if !result.Passed {
							return fmt.Errorf("safe %s failed: %s", name, result.Failure)
						}
						entry.SafePassed++
					} else if result.Passed {
						entry.UnsafePassed++
					} else {
						entry.UnsafeFailed++
						if retained == nil {
							copy := trial
							retained = &copy
							fingerprint = result.Checker
						}
					}
				}
			}
			if retained == nil {
				return fmt.Errorf("unsafe control never failed: %s", name)
			}
			base.StateCheck.Expected = fingerprint
			reduced, e := agentrunner.MinimizeTrial(context.Background(), *retained, "state:destination", 512, func(ctx context.Context, t agentrunner.Trial) (agentrunner.Result, error) {
				return execute(t, "unsafe", base)
			})
			if e != nil {
				return e
			}
			if !reduced.Complete {
				return fmt.Errorf("minimization exhausted budget: %s", name)
			}
			manifest := agentrunner.Reproduction{Trial: reduced.Trial, CausalExecution: reduced.CausalExecution, Assertion: "state:destination", Checker: fingerprint, CheckIDs: base.StateCheck.IDs, RequiresSetup: true, Timeout: base.Timeout, CheckTimeout: base.StateCheck.Timeout, RestartAfterCall: base.RestartAfterCall}
			original := filepath.Join(out, name+"-export")
			if e = agentrunner.ExportReproduction(original, incident, p.ConfigPath, manifest); e != nil {
				return e
			}
			moved := filepath.Join(out, name+"-reproduction")
			if e = os.Rename(original, moved); e != nil {
				return e
			}
			loaded, e := agentrunner.LoadReproduction(moved)
			if e != nil {
				return e
			}
			portable := base
			portable.IncidentDir = filepath.Join(moved, "incident")
			portable.ConfigPath = ""
			portable.StateCheck.Expected = loaded.Checker
			for repeat := 0; repeat < 2; repeat++ {
				for _, mode := range []string{"unsafe", "safe"} {
					result, e := execute(loaded.Trial, mode, portable)
					if e != nil {
						return e
					}
					if result.InvalidRun || result.Process.ExitCode != 0 || result.Passed != (mode == "safe") {
						return fmt.Errorf("relocated %s %s failed: %s; %s", name, mode, result.Failure, result.Process.Stderr)
					}
					if e = result.CausalExecution.Validate(loaded.Trial.Causal.Graph, loaded.Trial.Causal.Schedule); e != nil {
						return e
					}
					if e = agentrunner.WriteJSON(filepath.Join(out, fmt.Sprintf("%s-relocated-%d-%s.json", name, repeat, mode)), result); e != nil {
						return e
					}
				}
			}
			entry.Minimized = true
			entry.Reproduction = moved
			r.Scenes = append(r.Scenes, entry)
			if e = write(); e != nil {
				return e
			}
			fmt.Printf("%s safe=%d unsafe_failed=%d unsafe_passed=%d invalid=%d minimized=true\n", name, entry.SafePassed, entry.UnsafeFailed, entry.UnsafePassed, entry.Invalid)
		}
	}
	if len(r.Scenes) == 0 {
		return fmt.Errorf("no scenarios selected")
	}
	r.Passed = true
	return write()
}
