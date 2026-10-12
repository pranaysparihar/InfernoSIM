// causalbenchmark validates real application consequences in a SQLite database
// and independent files. It intentionally includes broken and fixed clients.
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

type sceneResult struct {
	Scenario       string `json:"scenario"`
	Seed           int64  `json:"seed"`
	Schedules      int    `json:"schedules"`
	Cases          int    `json:"cases"`
	SafePassed     int    `json:"safe_passed"`
	UnsafeFailures int    `json:"unsafe_failures"`
	UnsafePassed   int    `json:"unsafe_passed"`
	Invalid        int    `json:"invalid"`
	Reproduction   string `json:"reproduction,omitempty"`
	Minimized      bool   `json:"minimized"`
}
type report struct {
	Version  int           `json:"version"`
	Platform string        `json:"platform"`
	Started  time.Time     `json:"started"`
	Duration time.Duration `json:"duration"`
	Passed   bool          `json:"passed"`
	Scenes   []sceneResult `json:"scenes"`
}

func main() {
	binary := flag.String("binary", "", "Built InfernoSIM executable (stdio connector)")
	python := flag.String("python", "python3", "Python with standard-library SQLite")
	app := flag.String("app", "examples/concurrencylab/app.py", "Application fixture")
	out := flag.String("out", "causal-acceptance", "New output directory")
	iterations := flag.Int("iterations", 1, "Seeded campaign repetitions (1..100)")
	minimize := flag.Bool("minimize", true, "Reduce and repeat one retained regression per scenario")
	soak := flag.Duration("soak", 0, "Repeat retained regressions for this duration (release gate: 2h)")
	input := flag.String("reproductions", "", "Completed acceptance directory for -soak")
	flag.Parse()
	var err error
	if *soak > 0 {
		err = runSoak(*binary, *python, *app, *input, *out, *soak)
	} else {
		err = run(*binary, *python, *app, *out, *iterations, *minimize)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(binary, python, app, out string, iterations int, minimize bool) error {
	if binary == "" || iterations < 1 || iterations > 100 {
		return fmt.Errorf("requires -binary and iterations 1..100")
	}
	var err error
	binary, err = filepath.Abs(binary)
	if err != nil {
		return err
	}
	app, err = filepath.Abs(app)
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
	ctx := context.Background()
	for iteration := 0; iteration < iterations; iteration++ {
		for _, scene := range []string{"stale", "retry", "cancel", "compensate", "restart"} {
			seed := int64(7100 + iteration)
			name := fmt.Sprintf("%s-%d", scene, seed)
			incident := filepath.Join(out, name+"-incident")
			command := exec.Command(python, app, "--scenario", scene, "--init", incident)
			if output, err := command.CombinedOutput(); err != nil {
				return fmt.Errorf("fixture initialize: %w: %s", err, output)
			}
			var declarations causal.Declarations
			data, err := os.ReadFile(filepath.Join(incident, "declarations.json"))
			if err != nil {
				return err
			}
			if err = json.Unmarshal(data, &declarations); err != nil {
				return err
			}
			graph, err := agentrunner.DiscoverCausal(incident, declarations)
			if err != nil {
				return err
			}
			if err = agentrunner.WriteJSON(filepath.Join(out, name+"-graph.json"), graph); err != nil {
				return err
			}
			prepared, err := agentrunner.Prepare(incident, "", seed, 1, true)
			if err != nil {
				return err
			}
			plan, err := agentrunner.PlanExploration(prepared.Config.Agent, prepared.ScopeHash, agentrunner.ExploreOptions{Seed: seed, Budget: 200, MaxFaults: 2, CausalGraph: &graph, ScheduleBudget: 100, BoundaryTimeout: "5s"})
			if err != nil {
				return err
			}
			if plan.Truncated {
				return fmt.Errorf("acceptance fixture campaign unexpectedly truncated")
			}
			if err = agentrunner.WriteJSON(filepath.Join(out, name+"-plan.json"), plan); err != nil {
				return err
			}
			entry := sceneResult{Scenario: scene, Seed: seed, Schedules: len(plan.CausalPlan.Schedules), Cases: len(plan.Trials)}
			base := agentrunner.Options{IncidentDir: incident, ConfigPath: prepared.ConfigPath, Timeout: 10 * time.Second, MCPConnector: []string{binary, "agent", "mcp", "connect"}, StateCheck: agentrunner.StateCheckOptions{SetupCommand: []string{python, app, "--scenario", scene, "--setup"}, Command: []string{python, app, "--scenario", scene, "--check"}, IDs: []string{"database", "filesystem"}, Timeout: 5 * time.Second}}
			if scene == "restart" {
				base.RestartAfterCall = 1
			}
			execute := func(trial agentrunner.Trial, mode string) (agentrunner.Result, error) {
				opts := base
				opts.Command = []string{python, app, "--scenario", scene, "--mode", mode}
				opts, err := agentrunner.RunTrial(opts, prepared.Config.Agent, trial)
				if err != nil {
					return agentrunner.Result{}, err
				}
				return agentrunner.Run(ctx, opts)
			}
			var retained *agentrunner.Trial
			var proof *causal.Execution
			for _, trial := range plan.Trials {
				for _, mode := range []string{"safe", "unsafe"} {
					result, err := execute(trial, mode)
					if err != nil {
						return err
					}
					if result.InvalidRun || result.Process.ExitCode != 0 || result.Process.TimedOut {
						entry.Invalid++
						r.Scenes = append(r.Scenes, entry)
						_ = agentrunner.WriteJSON(filepath.Join(out, name+"-invalid.json"), result)
						return fmt.Errorf("%s %s invalid: %s; stderr=%s", scene, mode, result.Failure, result.Process.Stderr)
					}
					if mode == "safe" {
						if !result.Passed {
							r.Scenes = append(r.Scenes, entry)
							_ = agentrunner.WriteJSON(filepath.Join(out, name+"-safe-failure.json"), result)
							return fmt.Errorf("fixed %s failed: %s", scene, result.Failure)
						}
						entry.SafePassed++
					} else if result.Passed {
						entry.UnsafePassed++
					} else {
						entry.UnsafeFailures++
						if retained == nil {
							copy := trial
							retained = &copy
							proof = result.CausalExecution
						}
					}
				}
			}
			if entry.UnsafeFailures == 0 {
				return fmt.Errorf("campaign did not detect deliberate %s defect", scene)
			}
			if minimize {
				reduced, err := agentrunner.MinimizeTrial(ctx, *retained, "state:database", 80, func(ctx context.Context, t agentrunner.Trial) (agentrunner.Result, error) {
					return execute(t, "unsafe")
				})
				if err != nil {
					return err
				}
				if !reduced.Complete {
					return fmt.Errorf("%s minimization exceeded acceptance budget", scene)
				}
				retained = &reduced.Trial
				proof = reduced.CausalExecution
				entry.Minimized = true
				for repeat := 0; repeat < 2; repeat++ {
					for _, mode := range []string{"safe", "unsafe"} {
						result, err := execute(*retained, mode)
						if err != nil {
							return err
						}
						if result.InvalidRun || result.Process.ExitCode != 0 || result.Passed != (mode == "safe") {
							_ = agentrunner.WriteJSON(filepath.Join(out, name+"-retained-failure.json"), result)
							return fmt.Errorf("retained %s regression did not distinguish broken/fixed clients", scene)
						}
					}
				}
			}
			entry.Reproduction = filepath.Join(out, name+"-reproduction")
			manifest := agentrunner.Reproduction{Trial: *retained, CausalExecution: proof, Assertion: "state:database", CheckIDs: base.StateCheck.IDs, RequiresSetup: true, Timeout: base.Timeout, CheckTimeout: base.StateCheck.Timeout, RestartAfterCall: base.RestartAfterCall}
			if err := agentrunner.ExportReproduction(entry.Reproduction, incident, prepared.ConfigPath, manifest); err != nil {
				return err
			}
			r.Scenes = append(r.Scenes, entry)
			if err := write(); err != nil {
				return err
			}
			fmt.Printf("%s seed=%d schedules=%d safe=%d unsafe_failures=%d invalid=%d minimized=%t\n", scene, seed, entry.Schedules, entry.SafePassed, entry.UnsafeFailures, entry.Invalid, entry.Minimized)
		}
	}
	r.Passed = true
	return write()
}
