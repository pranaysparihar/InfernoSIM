package stubproxy

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"infernosim/pkg/agentreliability"
)

func TestAgentDelayAndTimeoutHonorCancellation(t *testing.T) {
	for _, decision := range []agentreliability.Decision{{Delay: time.Minute}, {Timeout: time.Minute}} {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		started := time.Now()
		recorder := httptest.NewRecorder()
		if !(&StubProxy{}).writeAgentDecision(ctx, recorder, decision, nil, "") {
			t.Fatal("cancelled response not handled")
		}
		if time.Since(started) > time.Second || recorder.Body.Len() != 0 {
			t.Fatal("cancelled fault slept or emitted a response")
		}
	}
}
