package stubproxy

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/http"
	"time"

	"infernosim/pkg/agentreliability"
	"infernosim/pkg/causal"
	"infernosim/pkg/event"
)

// serveCausal selects by a bound graph identity rather than by arrival order.
// Each HTTP handler waits independently, so other handlers can still arrive.
func (s *StubProxy) serveCausal(w http.ResponseWriter, r *http.Request, body []byte) {
	c := s.agentEngine.Causal()
	host := r.URL.Hostname()
	if host == "" {
		host = r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
	}
	fp, identity, runtimeID := causal.RequestFingerprint("http", r.Method, host, r.URL.RequestURI(), body)
	if h := r.Header.Get(causal.IdentityHeader); h != "" {
		identity = h
	}
	op, err := c.Bind(fp, identity, runtimeID)
	if err != nil {
		s.agentFailure(w, err)
		return
	}
	var expected *event.Event
	for i := range s.events {
		if "event:"+s.events[i].ID == op.Record {
			expected = &s.events[i]
			break
		}
	}
	if expected == nil {
		s.agentFailure(w, c.Fail(fmt.Errorf("causal record %s is missing", op.Record)))
		return
	}
	x, err := causal.HTTPExchange(*expected)
	if err == nil {
		err = c.Verify(op, x)
	}
	if err != nil {
		s.agentFailure(w, c.Fail(err))
		return
	}
	if isGRPCRequest(r) {
		s.agentFailure(w, c.Fail(fmt.Errorf("causal replay does not support gRPC boundaries")))
		return
	}
	response := x.Response
	// Selectors keep the recorded call identity while transport IDs can change.
	request, err := causal.RemapResponse(body, op.RequestID)
	if err != nil {
		s.agentFailure(w, c.Fail(err))
		return
	}
	var decision agentreliability.Decision
	engineRequest := agentreliability.Request{Method: r.Method, Host: host, Path: r.URL.Path, Headers: r.Header.Clone(), Body: request}
	err = c.Step(r.Context(), causal.Admit(op.ID), func() error {
		var processErr error
		decision, processErr = s.agentEngine.ProcessContext(r.Context(), engineRequest, agentreliability.Response{Status: expected.Status, Headers: http.Header(expected.ResponseHeaders).Clone(), Body: response})
		return processErr
	})
	if err != nil {
		s.agentFailure(w, err)
		return
	}
	releaseContext := r.Context()
	if s.agentEngine.PrepareRelease(&decision) {
		releaseContext = context.WithoutCancel(r.Context())
	}
	if decision.Delay > 0 && !waitAgentContext(releaseContext, decision.Delay) {
		_ = c.Fail(r.Context().Err())
		return
	}
	decision.Delay = 0
	if decision.Timeout > 0 && !waitAgentContext(releaseContext, decision.Timeout) {
		_ = c.Fail(r.Context().Err())
		return
	}
	err = c.At(releaseContext, causal.Release(op.ID), func(boundaryContext context.Context) error {
		if deadline, ok := boundaryContext.Deadline(); ok {
			_ = http.NewResponseController(w).SetWriteDeadline(deadline)
			defer http.NewResponseController(w).SetWriteDeadline(time.Time{})
		}
		delivered := false
		defer func() { s.agentEngine.CompleteResponse(engineRequest, decision, delivered) }()
		if decision.Timeout > 0 {
			http.Error(w, "injected agent dependency timeout", http.StatusGatewayTimeout)
			return nil
		}
		var remapErr error
		decision.Response.Body, remapErr = causal.RemapResponse(decision.Response.Body, runtimeID)
		if remapErr != nil {
			return remapErr
		}
		// Runtime JSON-RPC IDs may change byte length even without a fault.
		decision.Response.Headers.Del("Content-Length")
		if decision.ResponseLost || decision.Reset {
			s.writeAgentDecision(boundaryContext, w, decision, nil, "", nil)
			return nil
		}
		if len(expected.ResponseFrames) > 0 {
			frames := expected.ResponseFrames
			if len(decision.AppliedFaults) > 0 || !bytes.Equal(decision.Response.Body, response) {
				var err error
				frames, err = reframeResponse(frames, decision.Response.Body)
				if err != nil {
					return err
				}
			}
			err := writeCapturedResponseFrames(boundaryContext, w, decision.Response.Status, decision.Response.Headers, http.Header(expected.ResponseTrailers), frames)
			delivered = err == nil
			return err
		}
		copyHeaders(w.Header(), decision.Response.Headers)
		for name := range expected.ResponseTrailers {
			w.Header().Add("Trailer", name)
		}
		w.WriteHeader(decision.Response.Status)
		if _, err := w.Write(decision.Response.Body); err != nil {
			return err
		}
		for name, values := range expected.ResponseTrailers {
			w.Header()[name] = append([]string(nil), values...)
		}
		if flush, ok := w.(http.Flusher); ok {
			flush.Flush()
		}
		delivered = true
		return nil
	})
	if err != nil {
		s.agentFailure(w, err)
	}
}
