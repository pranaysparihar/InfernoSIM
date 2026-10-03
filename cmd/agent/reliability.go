package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"infernosim/pkg/agentreliability"
	"infernosim/pkg/agentrunner"
	"infernosim/pkg/reporting"
)

func runAgentCompare(args []string) int {
	fs := flag.NewFlagSet("agent compare", flag.ContinueOnError)
	base := fs.String("baseline-command-json", "", "Explicit baseline argv JSON array (no shell evaluation)")
	candidate := fs.String("candidate-command-json", "", "Explicit candidate argv JSON array")
	config := fs.String("config", "", "Replay configuration")
	seed := fs.Int64("seed", 0, "Case seed")
	budget := fs.Int("budget", 100, "Case budget")
	out := fs.String("report-dir", "./infernosim-agent-comparison", "Private report directory")
	timeout := fs.Duration("timeout", agentrunner.DefaultTimeout, "Timeout per application run")
	restart := fs.Int("restart-after-call", 0, "Crash/restart boundary for both applications")
	stateFlags := registerStateFlags(fs)
	incident, flags := positionalBeforeFlags(args)
	if err := fs.Parse(flags); err != nil {
		return 2
	}
	if incident == "" && fs.NArg() > 0 {
		incident = fs.Arg(0)
	}
	stateCheck, err := stateFlags.options()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	var commands [2][]string
	if incident == "" || json.Unmarshal([]byte(*base), &commands[0]) != nil || json.Unmarshal([]byte(*candidate), &commands[1]) != nil || len(commands[0]) == 0 || len(commands[1]) == 0 {
		fmt.Fprintln(os.Stderr, "agent compare requires incident and two nonempty JSON argv arrays")
		return 2
	}
	prepared, err := agentrunner.Prepare(incident, *config, *seed, *budget, true)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	var runs [2][]agentrunner.Result
	for _, c := range prepared.Cases {
		for side, command := range commands {
			fmt.Printf("Comparing %s side %d\n", c.ID, side)
			r, err := agentrunner.Run(context.Background(), agentrunner.Options{IncidentDir: incident, ConfigPath: prepared.ConfigPath, Case: c, Command: command, Timeout: *timeout, RestartAfterCall: *restart, StateCheck: stateCheck})
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				return 1
			}
			runs[side] = append(runs[side], r)
		}
	}
	r, err := agentrunner.CompareResults(runs[0], runs[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	for side, name := range []string{"baseline", "candidate"} {
		if err := agentrunner.WriteJSON(filepath.Join(*out, name+".json"), runs[side]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	}
	if err := agentrunner.WriteJSON(filepath.Join(*out, "comparison.json"), r); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	report := agentrunner.ReportingResult(runs[1])
	for _, message := range r.Regressions {
		report.Findings = append(report.Findings, reporting.Finding{RuleID: "AGENT_COMPARISON_REGRESSION", Level: "error", Title: "Reliability regression", Message: message})
		report.Cases = append(report.Cases, reporting.Case{ID: "comparison-regression", Name: message, Passed: false, Message: message})
	}
	if !r.Passed {
		report.Outcome = "FAIL_AGENT_COMPARISON"
	}
	if _, err := reporting.WriteFormats(*out, []string{"junit", "sarif", "html"}, report); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Printf("Comparison: passed=%t regressions=%d improvements=%d candidate_failures=%d\n", r.Passed, len(r.Regressions), len(r.Improvements), len(r.CandidateFailures))
	if !r.Passed {
		return 1
	}
	return 0
}

func runAgentReduce(args []string) int {
	flags, command := splitCommand(args)
	fs := flag.NewFlagSet("agent reduce", flag.ContinueOnError)
	config := fs.String("config", "", "Replay configuration")
	caseID := fs.String("case", "", "Case ID or single fault ID")
	assertion := fs.String("assertion", "", "Named violated assertion to preserve")
	seed := fs.Int64("seed", 0, "Case seed")
	budget := fs.Int("budget", 20, "Maximum application executions (2-100)")
	out := fs.String("out", "./infernosim-reduction.json", "Private reduction report")
	timeout := fs.Duration("timeout", agentrunner.DefaultTimeout, "Timeout per run")
	stateFlags := registerStateFlags(fs)
	incident, remaining := positionalBeforeFlags(flags)
	if err := fs.Parse(remaining); err != nil {
		return 2
	}
	if incident == "" && fs.NArg() > 0 {
		incident = fs.Arg(0)
	}
	if incident == "" || *caseID == "" || len(command) == 0 {
		fmt.Fprintln(os.Stderr, "agent reduce requires incident --case --assertion and explicit command after --")
		return 2
	}
	stateCheck, err := stateFlags.options()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	p, err := agentrunner.Prepare(incident, *config, *seed, agentreliability.MaximumCases, true)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	c, err := agentrunner.ResolveCase(p.Cases, *caseID)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	r, err := agentrunner.ReduceFaults(context.Background(), c, *assertion, *budget, func(ctx context.Context, c agentreliability.Case) (agentrunner.Result, error) {
		return agentrunner.Run(ctx, agentrunner.Options{IncidentDir: incident, ConfigPath: p.ConfigPath, Case: c, Command: command, Timeout: *timeout, StateCheck: stateCheck})
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := agentrunner.WriteJSON(*out, r); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Printf("Reduced to %d faults in %d executions; 1-minimal=%t; report=%s\n", len(r.Case.ActiveFaults()), r.Attempts, r.Complete, *out)
	if !r.Complete {
		return 1
	}
	return 0
}
