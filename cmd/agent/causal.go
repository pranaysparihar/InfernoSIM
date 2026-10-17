package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"infernosim/pkg/agentrunner"
	"infernosim/pkg/causal"
	"infernosim/pkg/privacy"
)

func readCausalJSON(path string, value any) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 16<<20+1))
	if err != nil {
		return err
	}
	if len(data) > 16<<20 {
		return fmt.Errorf("causal input exceeds 16 MiB")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	d.DisallowUnknownFields()
	if err = d.Decode(value); err != nil {
		return err
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return fmt.Errorf("expected one JSON document")
	}
	return nil
}

func runAgentCausal(args []string) int {
	if len(args) > 0 && args[0] == "record" {
		return runCausalRecord(args[1:])
	}
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "Usage: infernosim agent causal <record|convert|discover|plan> <incident-or-graph> [flags]")
		return 2
	}
	mode := args[0]
	fs := flag.NewFlagSet("agent causal "+mode, flag.ContinueOnError)
	out := fs.String("out", "", "New private graph or plan JSON file")
	declarations := fs.String("declarations", "", "Reviewed dependency, identity and issue resolution declarations JSON")
	review := fs.String("review", "", "Record why unobserved application dependencies permit this exploration")
	seed := fs.Int64("seed", 0, "Deterministic topological traversal seed")
	budget := fs.Int("budget", 16, "Schedule budget (1..1000)")
	input, rest := positionalBeforeFlags(args[1:])
	if err := fs.Parse(rest); err != nil {
		return 2
	}
	if input == "" && fs.NArg() > 0 {
		input = fs.Arg(0)
	}
	fail := func(err error) int { fmt.Fprintln(os.Stderr, "agent causal:", err); return 1 }
	if input == "" || *out == "" {
		return fail(fmt.Errorf("requires input and --out"))
	}
	if _, err := os.Lstat(*out); err == nil {
		return fail(fmt.Errorf("refusing to overwrite %s", *out))
	} else if !os.IsNotExist(err) {
		return fail(err)
	}
	switch mode {
	case "convert":
		d := causal.Declarations{}
		if *declarations != "" {
			if err := readCausalJSON(*declarations, &d); err != nil {
				return fail(err)
			}
		}
		if *review != "" {
			d.Review = *review
		}
		r, err := agentrunner.ConvertObserved(input, d)
		if err != nil {
			return fail(err)
		}
		if err = agentrunner.WriteJSON(*out, r); err != nil {
			return fail(err)
		}
		fmt.Printf("Capture conversion: %s; boundaries=%d; output=%s\n", r.Status, len(r.Boundaries), *out)
		for _, issue := range r.Issues {
			fmt.Println(issue)
		}
		if r.Schedule == nil {
			return 1
		}
		return 0
	case "discover":
		d := causal.Declarations{}
		if *declarations != "" {
			if err := readCausalJSON(*declarations, &d); err != nil {
				return fail(err)
			}
		}
		if *review != "" {
			d.Review = *review
		}
		g, err := agentrunner.DiscoverCausal(input, d)
		if err != nil {
			return fail(err)
		}
		if err = agentrunner.WriteJSON(*out, g); err != nil {
			return fail(err)
		}
		fmt.Printf("Discovered %d operations and %d evidenced edges; graph=%s\n", len(g.Operations), len(g.Edges), *out)
		for _, issue := range g.Issues {
			fmt.Printf("%s blocking=%t resolved=%t: %s\n", issue.ID, issue.Blocking, issue.Resolution != "", issue.Detail)
		}
		if err = g.Validate(true); err != nil {
			fmt.Fprintln(os.Stderr, "Graph saved; review required:", err)
			return 1
		}
		return 0
	case "plan":
		g, err := agentrunner.ReadGraph(input)
		if err != nil {
			return fail(err)
		}
		plan, err := causal.Generate(g, *seed, *budget)
		if err != nil {
			return fail(err)
		}
		if err = agentrunner.WriteJSON(*out, plan); err != nil {
			return fail(err)
		}
		fmt.Printf("Planned %d schedules; exhaustive=%t truncated=%t duplicate_candidates=%d\n", len(plan.Schedules), plan.Exhaustive, plan.Truncated, plan.Duplicates)
		return 0
	default:
		return fail(fmt.Errorf("unknown causal subcommand %q", mode))
	}
}

func runCausalRecord(args []string) int {
	flags, command := splitCommand(args)
	fs := flag.NewFlagSet("agent causal record", flag.ContinueOnError)
	mcp := fs.String("mcp-command-json", "", "Explicit test MCP server argv; application uses INFERNOSIM_MCP_COMMAND_JSON")
	sensitive := fs.Bool("capture-sensitive-data", false, "Explicitly retain request/response bodies; use synthetic test data")
	policyPath := fs.String("privacy-policy", "", "Privacy policy applied before capture storage")
	private := fs.Bool("allow-private-destinations", false, "Explicitly allow local/private test HTTP destinations")
	timeout := fs.Duration("timeout", agentrunner.DefaultTimeout, "Maximum recording duration")
	dir, rest := positionalBeforeFlags(flags)
	if err := fs.Parse(rest); err != nil {
		return 2
	}
	fail := func(err error) int { fmt.Fprintln(os.Stderr, "causal record:", err); return 1 }
	var mcpCommand []string
	if *mcp != "" {
		if err := json.Unmarshal([]byte(*mcp), &mcpCommand); err != nil || len(mcpCommand) == 0 {
			return fail(fmt.Errorf("MCP command must be a nonempty JSON argv array"))
		}
	}
	var p *privacy.Policy
	if *policyPath != "" {
		var err error
		p, err = privacy.Load(*policyPath)
		if err != nil {
			return fail(err)
		}
	}
	_, err := agentrunner.Record(context.Background(), agentrunner.RecordOptions{Incident: dir, Command: command, MCPCommand: mcpCommand, Timeout: *timeout, CaptureSensitiveData: *sensitive, AllowPrivateDestinations: *private, Privacy: p})
	if err != nil {
		return fail(err)
	}
	fmt.Printf("Recorded shared HTTP/MCP observation boundaries in %s; review and convert before replay\n", dir)
	return 0
}
