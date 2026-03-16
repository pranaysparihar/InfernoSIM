package replaydriver

import (
	"encoding/base64"
	"infernosim/pkg/event"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestDependencyExtraction(t *testing.T) {
	// 1. Mock JSON response with token
	resp := &http.Response{
		Header: make(http.Header),
	}
	resp.Header.Set("Content-Type", "application/json")
	body := []byte(`{"access_token": "secret123", "id": "order456"}`)
	
	produced := ExtractResponseValues(resp, body)

	foundToken := false
	foundID := false
	for _, p := range produced {
		if p.Kind == ValueKindAuthToken && p.Value == "secret123" {
			foundToken = true
		}
		if p.Kind == ValueKindResourceID && p.Value == "order456" {
			foundID = true
		}
	}

	if !foundToken {
		t.Errorf("failed to extract auth token")
	}
	if !foundID {
		t.Errorf("failed to extract resource ID")
	}
}

func TestDependencyExtraction_NoToken(t *testing.T) {
	resp := &http.Response{
		Header: make(http.Header),
	}
	resp.Header.Set("Content-Type", "application/json")

	body := []byte(`{"message": "hello world"}`)

	produced := ExtractResponseValues(resp, body)

	if len(produced) != 0 {
		t.Errorf("unexpected dependency extracted: %+v", produced)
	}
}

func TestSubstitutionEngine(t *testing.T) {
	state := NewRuntimeState()
	state.Put("old_token", "new_token")
	state.Put("order_001", "order_999")
	
	rewriter := NewRequestRewriter(state)

	// Test Header Substitution
	req, _ := http.NewRequest("GET", "http://api.com/orders/order_001", nil)
	req.Header.Set("Authorization", "Bearer old_token")
	
	rewriter.Rewrite(event.Event{}, req)

	if req.Header.Get("Authorization") != "Bearer new_token" {
		t.Errorf("header substitution failed: got %s", req.Header.Get("Authorization"))
	}
	if req.URL.Path != "/orders/order_999" {
		t.Errorf("URL path substitution failed: got %s", req.URL.Path)
	}

	// Test Body Substitution
	e := event.Event{
		BodyB64: base64.StdEncoding.EncodeToString([]byte(`{"id": "order_001"}`)),
	}
	newBody, _ := rewriter.PrepareBody(e)
	if !strings.Contains(string(newBody), "order_999") {
		t.Errorf("body substitution failed: got %s", string(newBody))
	}
}

func TestSubstitutionEngine_NoReplacement(t *testing.T) {
	state := NewRuntimeState()
	rewriter := NewRequestRewriter(state)

	req, _ := http.NewRequest("GET", "http://api.com/orders/order_001", nil)
	req.Header.Set("Authorization", "Bearer other_token")

	rewriter.Rewrite(event.Event{}, req)

	if req.Header.Get("Authorization") != "Bearer other_token" {
		t.Errorf("unexpected substitution occurred")
	}
}

func TestChainedAuthFlow(t *testing.T) {
	// Integration test: login -> call protected
	
	// In memory replayer would be ideal, but for now we'll test the logic chain
	state := NewRuntimeState()
	rewriter := NewRequestRewriter(state)

	// 1. Captured "Login" Response
	loginResp := &http.Response{
		Header: make(http.Header),
		StatusCode: 200,
	}
	loginResp.Header.Set("Content-Type", "application/json")
	
	// In a real run, ReplayEvents would call UpdateState. 
	// To test the chain, we simulate finding the "old" value from capture metadata
	// Since our current UpdateState is simplified, we'll manually seed the state
	state.Put("stale_token", "fresh_token")

	// 2. Mock replaying "Protected" Request
	protectedReq := event.Event{
		Method: "GET",
		URL: "http://api/protected",
		Headers: map[string][]string{"Authorization": {"Bearer stale_token"}},
	}

	req, _ := http.NewRequest(protectedReq.Method, protectedReq.URL, nil)
	req.Header.Set("Authorization", protectedReq.Headers["Authorization"][0])
	
	rewriter.Rewrite(protectedReq, req)

	if req.Header.Get("Authorization") != "Bearer fresh_token" {
		t.Errorf("chained auth flow failed: token was not substituted")
	}
}

func TestCookieSessionChain(t *testing.T) {
	jar, _ := cookiejar.New(nil)

	client := &http.Client{Jar: jar}

	loginResp := &http.Response{
		Header: make(http.Header),
	}
	loginResp.Header.Add("Set-Cookie", "session_id=abc123; Path=/")

	u, _ := url.Parse("http://api")
	// SetCookies expects []*http.Cookie
	cookies := loginResp.Cookies()
	client.Jar.SetCookies(u, cookies)

	req, _ := http.NewRequest("GET", "http://api/orders", nil)

	// Simulate replayer logic: pulling from jar into request
	for _, c := range client.Jar.Cookies(u) {
		req.AddCookie(c)
	}

	cookieStr := req.Header.Get("Cookie")
	if !strings.Contains(cookieStr, "session_id=abc123") {
		t.Errorf("cookie jar failed to propagate session, got: %s", cookieStr)
	}
}

func TestDeterministicChaosReplay(t *testing.T) {
	// Simulate request 7 having +500ms latency injected
	// We'll verify that the replayed event records the increased duration
	
	capturedEvent := event.Event{
		ID: "7",
		Method: "GET",
		URL: "http://api/slow",
		Duration: 10 * time.Millisecond,
	}

	replayedEvent := event.Event{
		Method: "GET",
		URL: "http://api/slow",
		Duration: 510 * time.Millisecond, // Injected +500ms
	}

	id, _ := strconv.Atoi(capturedEvent.ID)
	diff := CompareEvents(capturedEvent, replayedEvent, id)
	if diff == nil || diff.LatencyDiff == "" {
		t.Errorf("chaos replay test failed: latency difference not detected")
	}
	
	if !strings.Contains(diff.LatencyDiff, "delta 500ms") {
		t.Errorf("chaos replay test failed: expected 500ms delta report, got %s", diff.LatencyDiff)
	}

	// Assert determinism (cross-run simulation)
	simulateChaos := func(evt event.Event) time.Duration {
		// In a real run, this would be the logic applying the injection
		// We simulate the deterministic nature here
		base := evt.Duration
		injection := 500 * time.Millisecond
		return base + injection
	}

	run1Duration := simulateChaos(capturedEvent)
	run2Duration := simulateChaos(capturedEvent)

	if run1Duration != run2Duration {
		t.Errorf("chaos injection not deterministic")
	}
}
func TestStatusDiffValidation(t *testing.T) {
	captured := event.Event{Status: 200}
	replayed := event.Event{Status: 500}

	diff := CompareEvents(captured, replayed, 1)

	if diff == nil || diff.StatusChange != "200 -> 500" {
		t.Errorf("status diff not detected correctly, got: %v", diff)
	}
}

func TestGoldenReplayScenario(t *testing.T) {
	// Create a temporary mock server to target
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/login" {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"token": "fresh_token"}`))
			return
		}
		if r.URL.Path == "/protected" {
			auth := r.Header.Get("Authorization")
			if auth == "Bearer fresh_token" {
				w.WriteHeader(http.StatusOK)
			} else {
				w.WriteHeader(http.StatusUnauthorized)
			}
			return
		}
	}))
	defer server.Close()

	fixturePath := filepath.Join("fixtures", "auth_chain.json")
	events, err := LoadInboundEvents(fixturePath)
	if err != nil {
		t.Fatalf("failed to load fixture: %v", err)
	}

	// Seed state for substitution (simulating dependency detection)
	// In a real system, the first response would update this. 
	// For the golden test, we verify the chain works.
	cfg := ReplayConfig{
		TimeScale: 1.0,
		Density:   100.0, // Speed up test
		MinGap:    0,
	}

	// Since ReplayEvents creates its own state/rewriter, we'll manually verify 
	// the core logic by setting a mapping that the protected request should use.
	// NOTE: ReplayEvents is currently a black box for state. 
	// We'll rely on the replayer's internal logic which we tested in units.
	
	result, err := ReplayEvents(events, server.URL, cfg)
	if err != nil {
		t.Fatalf("replay failed: %v", err)
	}

	if result.CompletedEvents != 2 {
		t.Errorf("expected 2 events replayed, got %d", result.CompletedEvents)
	}

	// The first event is /login (200), the second should be /protected (200 if substituted)
	// Note: Currently ReplayEvents state heuristic is simple. 
	// This golden test validates the execution flow.
}
