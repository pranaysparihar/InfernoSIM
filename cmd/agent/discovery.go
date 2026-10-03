package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"infernosim/pkg/agentrunner"
	"infernosim/pkg/reporting"
)

type explorationReport struct {
	Version           int                         `json:"version"`
	Plan              agentrunner.ExplorationPlan `json:"plan"`
	Passed            int                         `json:"passed"`
	Failed            int                         `json:"failed"`
	Invalid           int                         `json:"invalid"`
	CoveredAssertions map[string]int              `json:"covered_assertions"`
	Results           []agentrunner.Result        `json:"results"`
	Reproductions     []string                    `json:"reproductions"`
}

func runAgentExplore(args []string) int {
	flags, command := splitCommand(args)
	fs := flag.NewFlagSet("agent explore", flag.ContinueOnError)
	config := fs.String("config", "", "Replay configuration")
	seed := fs.Int64("seed", 0, "Deterministic exploration traversal seed")
	budget := fs.Int("budget", 100, "Maximum application executions (1..1000)")
	depth := fs.Int("max-faults", 3, "Maximum simultaneous selected faults (1..4)")
	occurrences := fs.Int("occurrences", 0, "Also explore occurrence positions 1..N (0..16)")
	permute := fs.String("permute-schedule", "", "Explicitly allow permutations of this schedule's independent calls (at most six)")
	out := fs.String("report-dir", "./infernosim-exploration", "New private report directory; never overwrite")
	timeout := fs.Duration("timeout", agentrunner.DefaultTimeout, "Application timeout per case")
	restart := fs.Int("restart-after-call", 0, "Explicit restart boundary, retained by reproductions")
	planOnly := fs.Bool("plan-only", false, "Write the bounded plan without executing")
	sf := registerStateFlags(fs)
	incident, rest := positionalBeforeFlags(flags)
	if err := fs.Parse(rest); err != nil {
		return 2
	}
	if incident == "" && fs.NArg() > 0 {
		incident = fs.Arg(0)
	}
	fail := func(err error) int { fmt.Fprintln(os.Stderr, "agent explore:", err); return 1 }
	if incident == "" || (!*planOnly && len(command) == 0) {
		return fail(fmt.Errorf("requires incident and explicit application command after -- (unless --plan-only)"))
	}
	check, err := sf.options()
	if err != nil {
		return fail(err)
	}
	p, err := agentrunner.Prepare(incident, *config, *seed, 1, true)
	if err != nil {
		return fail(err)
	}
	plan, err := agentrunner.PlanExploration(p.Config.Agent, p.ScopeHash, agentrunner.ExploreOptions{Seed: *seed, Budget: *budget, MaxFaults: *depth, Occurrences: *occurrences, PermuteSchedule: *permute})
	if err != nil {
		return fail(err)
	}
	if *timeout < time.Millisecond || *timeout > agentrunner.MaximumTimeout {
		return fail(fmt.Errorf("invalid timeout"))
	}
	if err = os.MkdirAll(filepath.Dir(*out), 0o700); err != nil {
		return fail(err)
	}
	if err = os.Mkdir(*out, 0o700); err != nil {
		return fail(err)
	}
	if err = agentrunner.WriteJSON(filepath.Join(*out, "plan.json"), plan); err != nil {
		return fail(err)
	}
	fmt.Printf("Planned %d cases; truncated=%t; seed=%d\n", len(plan.Trials), plan.Truncated, *seed)
	if *planOnly {
		return 0
	}
	report := explorationReport{Version: 1, Plan: plan, CoveredAssertions: map[string]int{}}
	for _, trial := range plan.Trials {
		opts, e := agentrunner.RunTrial(agentrunner.Options{IncidentDir: incident, ConfigPath: p.ConfigPath, Command: command, Timeout: *timeout, RestartAfterCall: *restart, StateCheck: check}, p.Config.Agent, trial)
		if e != nil {
			return fail(e)
		}
		result, e := agentrunner.Run(context.Background(), opts)
		if e != nil {
			return fail(e)
		}
		report.Results = append(report.Results, result)
		if result.InvalidRun {
			report.Invalid++
		} else if result.Passed {
			report.Passed++
		} else {
			report.Failed++
		}
		for _, a := range result.Assertions {
			if a.Coverage != "not_exercised" && !result.InvalidRun {
				report.CoveredAssertions[a.ID]++
			}
		}
		fmt.Printf("%s passed=%t invalid=%t\n", trial.Case.ID, result.Passed, result.InvalidRun)
		if !result.Passed && !result.InvalidRun && result.Process.ExitCode == 0 && !result.Process.TimedOut {
			assertion := ""
			for _, a := range result.Assertions {
				if !a.Passed && a.Coverage == "violated" {
					assertion = a.ID
					break
				}
			}
			if assertion != "" {
				destination := filepath.Join(*out, "reproductions", trial.Case.ID)
				manifest := agentrunner.Reproduction{Trial: trial, Assertion: assertion, CheckIDs: check.IDs, RequiresSetup: len(check.SetupCommand) > 0, Timeout: *timeout, CheckTimeout: *sf.timeout, RestartAfterCall: *restart}
				if e = agentrunner.ExportReproduction(destination, incident, p.ConfigPath, manifest); e != nil {
					return fail(e)
				}
				report.Reproductions = append(report.Reproductions, destination)
			}
		}
		// Persist after every case so interruption does not discard earlier evidence.
		if e = agentrunner.WriteJSON(filepath.Join(*out, "exploration.json"), report); e != nil {
			return fail(e)
		}
	}
	if _, err = reporting.WriteFormats(*out, []string{"junit", "sarif", "html"}, agentrunner.ReportingResult(report.Results)); err != nil {
		return fail(err)
	}
	fmt.Printf("Exploration: passed=%d failed=%d invalid=%d truncated=%t reproductions=%d\n", report.Passed, report.Failed, report.Invalid, plan.Truncated, len(report.Reproductions))
	if report.Failed > 0 || report.Invalid > 0 {
		return 1
	}
	return 0
}
func runAgentReproduce(args []string, minimize bool) int {
	flags, command := splitCommand(args)
	fs := flag.NewFlagSet("agent reproduce/minimize", flag.ContinueOnError)
	out := fs.String("out", "", "New reduced reproduction directory (minimize only)")
	budget := fs.Int("budget", 100, "Maximum minimization executions (2..1000)")
	assertion := fs.String("assertion", "", "Named assertion to preserve (defaults to manifest)")
	reportDir := fs.String("report-dir", "", "Optional reproduction results directory outside the artifact")
	sf := registerStateFlags(fs)
	dir, rest := positionalBeforeFlags(flags)
	if err := fs.Parse(rest); err != nil {
		return 2
	}
	if dir == "" && fs.NArg() > 0 {
		dir = fs.Arg(0)
	}
	fail := func(err error) int { fmt.Fprintln(os.Stderr, "agent reproduction:", err); return 1 }
	if dir == "" || len(command) == 0 || (minimize && *out == "") {
		return fail(fmt.Errorf("requires artifact, explicit application command, and --out for minimize"))
	}
	manifest, err := agentrunner.LoadReproduction(dir)
	if err != nil {
		return fail(err)
	}
	check, err := sf.options()
	if err != nil {
		return fail(err)
	}
	want := append([]string{}, manifest.CheckIDs...)
	got := append([]string{}, check.IDs...)
	sort.Strings(want)
	sort.Strings(got)
	if !reflect.DeepEqual(want, got) || manifest.RequiresSetup != (len(check.SetupCommand) > 0) {
		return fail(fmt.Errorf("supply the same required state check IDs and setup contract explicitly"))
	}
	check.Timeout = manifest.CheckTimeout
	incident := filepath.Join(dir, "incident")
	p, err := agentrunner.Prepare(incident, "", 0, 1, true)
	if err != nil {
		return fail(err)
	}
	run := func(ctx context.Context, t agentrunner.Trial) (agentrunner.Result, error) {
		opts, e := agentrunner.RunTrial(agentrunner.Options{IncidentDir: incident, ConfigPath: p.ConfigPath, Command: command, Timeout: manifest.Timeout, RestartAfterCall: manifest.RestartAfterCall, StateCheck: check}, p.Config.Agent, t)
		if e != nil {
			return agentrunner.Result{}, e
		}
		return agentrunner.Run(ctx, opts)
	}
	if minimize {
		target := *assertion
		if target == "" {
			target = manifest.Assertion
		}
		reduced, e := agentrunner.MinimizeTrial(context.Background(), manifest.Trial, target, *budget, run)
		if e != nil {
			return fail(e)
		}
		manifest.Trial = reduced.Trial
		manifest.Assertion = target
		if e = agentrunner.ExportReproduction(*out, incident, p.ConfigPath, manifest); e != nil {
			return fail(e)
		}
		// Keep reducer metadata outside the integrity-checked artifact.
		if e = agentrunner.WriteJSON(*out+".minimization.json", reduced); e != nil {
			return fail(e)
		}
		fmt.Printf("Minimized: attempts=%d complete=%t artifact=%s\n", reduced.Attempts, reduced.Complete, *out)
		if !reduced.Complete {
			return 1
		}
		return 0
	}
	result, err := run(context.Background(), manifest.Trial)
	if err != nil {
		return fail(err)
	}
	if *reportDir != "" {
		artifact, _ := filepath.Abs(dir)
		report, _ := filepath.Abs(*reportDir)
		rel, _ := filepath.Rel(artifact, report)
		if rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return fail(fmt.Errorf("reports must be outside the reproduction artifact"))
		}
		if err = agentrunner.WriteJSON(filepath.Join(*reportDir, "result.json"), result); err != nil {
			return fail(err)
		}
		if _, err = reporting.WriteFormats(*reportDir, []string{"junit", "sarif", "html"}, agentrunner.ReportingResult([]agentrunner.Result{result})); err != nil {
			return fail(err)
		}
	}
	for _, a := range result.Assertions {
		fmt.Printf("%s %s: %s\n", a.Coverage, a.ID, a.Message)
	}
	fmt.Printf("Reproduction: passed=%t invalid=%t\n", result.Passed, result.InvalidRun)
	if !result.Passed {
		return 1
	}
	return 0
}
