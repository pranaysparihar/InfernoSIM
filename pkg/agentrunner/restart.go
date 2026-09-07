package agentrunner

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
)

// executeWithRestart kills the explicitly supplied application at an observed
// simulator boundary. The simulator and a private checkpoint directory survive
// the restart. This is process-recovery testing, not disk/power-loss simulation.
func executeWithRestart(ctx context.Context, opts Options, env []string, boundary <-chan struct{}, resume chan struct{}) (ProcessResult, error) {
	defer close(resume)
	checkpoint, err := os.MkdirTemp("", "infernosim-checkpoint-*")
	if err != nil {
		return ProcessResult{}, err
	}
	defer os.RemoveAll(checkpoint)
	stdout := &boundedBuffer{limit: opts.OutputLimit}
	stderr := &boundedBuffer{limit: opts.OutputLimit}
	result := ProcessResult{}
	for attempt := 0; attempt < 2; attempt++ {
		childContext, stop := context.WithCancel(ctx)
		command := exec.CommandContext(childContext, opts.Command[0], opts.Command[1:]...)
		configureCommand(command)
		command.Env = mergeEnvironment(env, nil, map[string]string{"INFERNOSIM_CHECKPOINT_DIR": checkpoint, "INFERNOSIM_ATTEMPT": strconv.Itoa(attempt)})
		command.Stdout = stdout
		command.Stderr = stderr
		if err := command.Start(); err != nil {
			stop()
			return result, err
		}
		done := make(chan error, 1)
		go func() { done <- command.Wait() }()
		var runErr error
		if attempt == 0 && opts.RestartAfterCall > 0 {
			select {
			case runErr = <-done:
			case <-boundary:
				stop()
				<-done
				result.Restarted = true
				// Release the first request before starting recovery. Closing the
				// channel here would double-close on return; send one rendezvous.
				select {
				case resume <- struct{}{}:
				case <-ctx.Done():
				}
				if ctx.Err() != nil {
					return result, ctx.Err()
				}
				continue
			}
		} else {
			runErr = <-done
		}
		stop()
		result.Stdout = stdout.String()
		result.Stderr = stderr.String()
		result.OutputTruncated = stdout.truncated || stderr.truncated
		return result, runErr
	}
	return result, fmt.Errorf("restart attempt limit reached")
}
