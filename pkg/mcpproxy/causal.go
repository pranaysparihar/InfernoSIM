package mcpproxy

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"

	"infernosim/pkg/agentreliability"
	"infernosim/pkg/causal"
)

// Exchanges correlates responses by JSON-RPC ID, never by adjacency. Incomplete
// requests are supported only when a matching cancellation was captured.
func Exchanges(records []Record) ([]causal.Exchange, error) {
	var xs []causal.Exchange
	requests := map[string]int{}
	completed := map[string]bool{}
	cancelled := map[string]bool{}
	sequences := map[int64]bool{}
	for _, r := range records {
		if r.Sequence < 1 || sequences[r.Sequence] {
			return nil, fmt.Errorf("invalid or duplicate MCP transcript sequence %d", r.Sequence)
		}
		sequences[r.Sequence] = true
		v, err := causal.Decode(r.Message)
		if err != nil {
			return nil, err
		}
		m, ok := v.(map[string]any)
		if !ok || m["jsonrpc"] != "2.0" {
			return nil, fmt.Errorf("unsupported MCP JSON-RPC envelope at %d", r.Sequence)
		}
		id, hasID := messageID(r.Message)
		method, _ := m["method"].(string)
		if hasID {
			switch value := m["id"].(type) {
			case string:
				if value == "" {
					return nil, fmt.Errorf("empty MCP request ID")
				}
			case json.Number:
				if _, err := value.Int64(); err != nil {
					return nil, fmt.Errorf("MCP ID must be an integer or string")
				}
			default:
				return nil, fmt.Errorf("MCP ID must be an integer or string")
			}
		}
		if r.Direction == "client_to_server" {
			if method == "" {
				return nil, fmt.Errorf("client responses/server-initiated requests are outside causal replay support")
			}
			x := causal.Exchange{ID: fmt.Sprintf("mcp_%d", r.Sequence), Protocol: "mcp", Method: "STDIO", Host: "stdio.mcp", Path: "/stdio", Record: fmt.Sprintf("mcp:%d", r.Sequence), Request: append([]byte(nil), r.Message...), Notification: !hasID}
			if r.Received != nil || r.Forwarded != nil {
				x.RecordingHash = causal.Hash(r)
			}
			if hasID {
				if _, exists := requests[string(id)]; exists {
					return nil, fmt.Errorf("recorded MCP request ID reused; identities must be unique within a recording")
				}
				requests[string(id)] = len(xs)
			}
			if method == "notifications/cancelled" {
				p, _ := m["params"].(map[string]any)
				target, _ := json.Marshal(p["requestId"])
				index, ok := requests[string(target)]
				if !ok {
					return nil, fmt.Errorf("MCP cancellation references an unknown request")
				}
				targetValue, _ := causal.Decode(xs[index].Request)
				if targetValue.(map[string]any)["method"] == "initialize" {
					return nil, fmt.Errorf("MCP initialize cannot be cancelled")
				}
				cancelled[string(target)] = true
			}
			xs = append(xs, x)
		} else if r.Direction == "server_to_client" {
			if hasID {
				if method != "" {
					return nil, fmt.Errorf("MCP server-initiated requests are outside causal replay support")
				}
				index, exists := requests[string(id)]
				if !exists || completed[string(id)] {
					return nil, fmt.Errorf("MCP response has no unique pending request")
				}
				_, result := m["result"]
				_, failure := m["error"]
				if result == failure {
					return nil, fmt.Errorf("MCP response requires exactly one result or error")
				}
				xs[index].Response = append([]byte(nil), r.Message...)
				if r.Received != nil || r.Forwarded != nil {
					xs[index].RecordingHash = causal.Hash([]string{xs[index].RecordingHash, causal.Hash(r)})
				}
				completed[string(id)] = true
			} else {
				if method == "" {
					return nil, fmt.Errorf("MCP server notification requires method")
				}
				xs = append(xs, causal.Exchange{ID: fmt.Sprintf("mcp_%d", r.Sequence), Protocol: "mcp", Method: "STDIO-SERVER", Host: "stdio.mcp", Path: "/stdio", Record: fmt.Sprintf("mcp:%d", r.Sequence), Request: append([]byte(nil), r.Message...), Notification: true, ServerNotification: true})
				if r.Received != nil || r.Forwarded != nil {
					xs[len(xs)-1].RecordingHash = causal.Hash(r)
				}
			}
		} else {
			return nil, fmt.Errorf("unsupported transcript direction")
		}
	}
	for id := range requests {
		if !completed[id] && !cancelled[id] {
			return nil, fmt.Errorf("MCP recording has an incomplete uncancelled request")
		}
	}
	return xs, nil
}

// ReplayCausal consumes input independently from scheduled workers. It requires
// a closable input so cancellation and worker errors can unblock a pending read.
func ReplayCausal(opts ReplayOptions, records []Record) error {
	c := opts.Engine.Causal()
	if c == nil {
		return fmt.Errorf("causal MCP replay requires a graph and schedule")
	}
	input, ok := opts.Stdin.(io.ReadCloser)
	if !ok {
		return fmt.Errorf("causal MCP replay requires a closable input")
	}
	xs, err := Exchanges(records)
	if err != nil {
		return c.Fail(err)
	}
	byRecord := map[string]causal.Exchange{}
	for _, x := range xs {
		byRecord[x.Record] = x
	}
	ctx, cancel := context.WithCancel(opts.Context)
	defer cancel()
	stop := context.AfterFunc(ctx, func() {
		_ = input.Close()
		if out, ok := opts.Stdout.(io.Closer); ok {
			_ = out.Close()
		}
	})
	defer stop()
	var wg sync.WaitGroup
	var mu sync.Mutex
	runtimeToRecorded := map[string]json.RawMessage{}
	active := map[string]bool{}
	cancelled := map[string]bool{}
	var firstErr error
	fail := func(err error) {
		if err == nil {
			return
		}
		mu.Lock()
		if firstErr == nil {
			firstErr = err
		}
		mu.Unlock()
		_ = c.Fail(err)
		cancel()
	}
	launch := func(op causal.Operation, x causal.Exchange, request []byte, runtimeID string) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var decision agentreliability.Decision
			engineRequest := agentreliability.Request{Method: "STDIO", Host: "stdio.mcp", Path: "/stdio", Headers: make(http.Header), Body: request}
			err := c.Step(ctx, causal.Admit(op.ID), func() error {
				if op.ServerNotification {
					return nil
				}
				mu.Lock()
				inFlight := cancelled[op.RequestID]
				mu.Unlock()
				status := http.StatusOK
				if op.Notification {
					status = http.StatusNoContent
				}
				var processErr error
				engineRequest.InFlightAtCancel = inFlight
				decision, processErr = opts.Engine.ProcessContext(ctx, engineRequest, agentreliability.Response{Status: status, Headers: make(http.Header), Body: x.Response})
				return processErr
			})
			if err != nil {
				fail(err)
				return
			}
			opts.Engine.PrepareRelease(&decision)
			if err = wait(ctx, decision.Delay+decision.Timeout); err != nil {
				fail(err)
				return
			}
			err = c.At(ctx, causal.Release(op.ID), func(boundaryContext context.Context) error {
				stopWrite := context.AfterFunc(boundaryContext, func() {
					if closer, ok := opts.Stdout.(io.Closer); ok {
						_ = closer.Close()
					}
				})
				defer stopWrite()
				delivered := op.Notification
				defer func() {
					if !op.ServerNotification {
						opts.Engine.CompleteResponse(engineRequest, decision, delivered)
					}
				}()
				if runtimeID != "" {
					defer func() { mu.Lock(); delete(active, runtimeID); mu.Unlock() }()
				}
				if op.ServerNotification {
					_, err := opts.Stdout.Write(append(append([]byte(nil), x.Request...), '\n'))
					return err
				}
				if decision.Reset {
					return ErrResponseLost
				}
				if op.Notification || decision.ResponseLost || decision.Timeout > 0 || len(x.Response) == 0 {
					return nil
				}
				response, err := causal.RemapResponse(decision.Response.Body, runtimeID)
				if err != nil {
					return err
				}
				_, err = opts.Stdout.Write(append(response, '\n'))
				delivered = err == nil
				return err
			})
			if err != nil {
				fail(err)
			}
		}()
	}
	// Server notifications are scheduled participants, not output opportunistically
	// emitted while replaying some unrelated client's response.
	for _, x := range xs {
		if x.ServerNotification {
			fp, identity, _ := causal.RequestFingerprint(x.Protocol, x.Method, x.Host, x.Path, x.Request)
			op, err := c.Bind(fp, identity, "")
			if err != nil {
				fail(err)
				break
			}
			if err = c.Verify(op, x); err != nil {
				fail(err)
				break
			}
			launch(op, x, x.Request, "")
		}
	}
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 64*1024), MaximumMessageSize)
	for scanner.Scan() {
		if ctx.Err() != nil {
			break
		}
		request := append([]byte(nil), scanner.Bytes()...)
		v, err := causal.Decode(request)
		if err != nil {
			fail(err)
			break
		}
		m, ok := v.(map[string]any)
		if !ok || m["jsonrpc"] != "2.0" || m["method"] == nil {
			fail(fmt.Errorf("invalid MCP client message"))
			break
		}
		id, hasID := messageID(request)
		runtimeID := ""
		if hasID {
			valid := false
			switch value := m["id"].(type) {
			case string:
				valid = value != ""
			case json.Number:
				_, idErr := value.Int64()
				valid = idErr == nil
			}
			if !valid {
				fail(fmt.Errorf("MCP runtime ID must be a nonempty string or integer"))
				break
			}
			runtimeID = string(id)
		}
		mu.Lock()
		if hasID && active[runtimeID] {
			mu.Unlock()
			fail(fmt.Errorf("duplicate in-flight MCP runtime ID"))
			break
		}
		if m["method"] == "notifications/cancelled" {
			p, ok := m["params"].(map[string]any)
			if !ok {
				mu.Unlock()
				fail(fmt.Errorf("invalid cancellation parameters"))
				break
			}
			target, _ := json.Marshal(p["requestId"])
			recorded, exists := runtimeToRecorded[string(target)]
			if !exists {
				mu.Unlock()
				fail(fmt.Errorf("cancellation has unknown runtime target"))
				break
			}
			value, _ := causal.Decode(recorded)
			p["requestId"] = value
			if active[string(target)] {
				cancelled[string(recorded)] = true
			}
			request, _ = json.Marshal(m)
		}
		mu.Unlock()
		fp, identity, _ := causal.RequestFingerprint("mcp", "STDIO", "stdio.mcp", "/stdio", request)
		op, err := c.Bind(fp, identity, runtimeID)
		if err != nil {
			fail(err)
			break
		}
		x, exists := byRecord[op.Record]
		if !exists {
			fail(fmt.Errorf("causal MCP recording missing %s", op.Record))
			break
		}
		if err = c.Verify(op, x); err != nil {
			fail(err)
			break
		}
		if hasID == op.Notification {
			fail(fmt.Errorf("MCP request/notification shape changed"))
			break
		}
		if hasID {
			mu.Lock()
			active[runtimeID] = true
			runtimeToRecorded[runtimeID] = json.RawMessage(op.RequestID)
			mu.Unlock()
		}
		request, err = causal.RemapResponse(request, op.RequestID)
		if err != nil {
			fail(err)
			break
		}
		launch(op, x, request, runtimeID)
	}
	if err := scanner.Err(); err != nil && ctx.Err() == nil {
		fail(err)
	}
	wg.Wait()
	mu.Lock()
	err = firstErr
	mu.Unlock()
	if err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return c.Fail(err)
	}
	if !opts.SharedController {
		return c.Complete()
	}
	return nil
}
