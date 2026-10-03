package agentrunner

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"sort"
	"time"

	"infernosim/pkg/agentreliability"
)

// StateCheckOptions is supplied by the caller, never loaded as executable
// instructions from incident files. Probes query independently maintained test
// state; their stdout/stderr is not retained in reports.
type StateCheckOptions struct {
	SetupCommand []string
	Command      []string
	IDs          []string
	Timeout      time.Duration
}

var checkID = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,63}$`)

func (o StateCheckOptions) timeout() time.Duration {
	if o.Timeout == 0 {
		return 10 * time.Second
	}
	return o.Timeout
}
func (o StateCheckOptions) Validate() error {
	if o.timeout() < time.Millisecond || o.timeout() > time.Minute {
		return fmt.Errorf("state hook timeout must be 1ms..1m")
	}
	if len(o.Command) == 0 && (len(o.IDs) > 0 || len(o.SetupCommand) > 0) {
		return fmt.Errorf("state checks require an explicit check command")
	}
	if len(o.Command) > 0 && (len(o.IDs) == 0 || len(o.IDs) > 100) {
		return fmt.Errorf("state checks require 1..100 expected IDs")
	}
	for _, argv := range [][]string{o.Command, o.SetupCommand} {
		if len(argv) > 0 && argv[0] == "" {
			return fmt.Errorf("state hook command cannot be empty")
		}
	}
	seen := map[string]bool{}
	for _, id := range o.IDs {
		if !checkID.MatchString(id) || seen[id] {
			return fmt.Errorf("invalid or duplicate state check ID")
		}
		seen[id] = true
	}
	return nil
}
func (o StateCheckOptions) Hash() string {
	if len(o.Command) == 0 {
		return ""
	}
	ids := append([]string(nil), o.IDs...)
	sort.Strings(ids)
	return agentreliability.StableHash(struct {
		Setup, Command, IDs []string
		Timeout             time.Duration
	}{o.SetupCommand, o.Command, ids, o.timeout()})
}
func runStateCommand(ctx context.Context, argv, env []string, timeout time.Duration) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	configureCommand(cmd)
	cmd.WaitDelay = time.Second
	cmd.Env = env
	output := &boundedBuffer{limit: 64 * 1024}
	cmd.Stdout = output
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("hook did not complete successfully")
	}
	if ctx.Err() != nil || output.truncated {
		return nil, fmt.Errorf("hook timed out or exceeded 64 KiB")
	}
	return []byte(output.String()), nil
}
func evaluateStateCheck(ctx context.Context, opts StateCheckOptions, env []string) ([]agentreliability.AssertionResult, error) {
	data, err := runStateCommand(ctx, opts.Command, env, opts.timeout())
	if err != nil {
		return nil, err
	}
	var payload struct {
		Assertions []struct {
			ID     string `json:"id"`
			Passed *bool  `json:"passed"`
		} `json:"assertions"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		return nil, fmt.Errorf("expected JSON assertions with id and passed fields")
	}
	if decoder.Decode(new(any)) != io.EOF {
		return nil, fmt.Errorf("unexpected data after state check JSON")
	}
	if len(payload.Assertions) != len(opts.IDs) {
		return nil, fmt.Errorf("state check omitted or added expected IDs")
	}
	expected := map[string]bool{}
	for _, id := range opts.IDs {
		expected[id] = true
	}
	var results []agentreliability.AssertionResult
	for _, a := range payload.Assertions {
		if !expected[a.ID] || a.Passed == nil {
			return nil, fmt.Errorf("state check has duplicate, unknown, or incomplete assertions")
		}
		delete(expected, a.ID)
		coverage, message := "exercised", "independent application-state check passed"
		if !*a.Passed {
			coverage, message = "violated", "independent application-state check failed"
		}
		results = append(results, agentreliability.AssertionResult{ID: "state:" + a.ID, Type: "application_state", Passed: *a.Passed, Coverage: coverage, Message: message})
	}
	sort.Slice(results, func(i, j int) bool { return results[i].ID < results[j].ID })
	return results, nil
}
