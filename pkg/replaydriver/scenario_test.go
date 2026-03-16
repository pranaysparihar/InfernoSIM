package replaydriver

import (
	"testing"
)

const testdataDir = "testdata/incidents"

// incidentDir returns the path to a named incident in the test corpus.
func incidentDir(name string) string {
	return testdataDir + "/" + name
}

// -----------------------------------------------------------------------
// Bundle loading
// -----------------------------------------------------------------------

func TestScenario_OpenBundle(t *testing.T) {
	scenarios := []string{
		"auth-token-chain",
		"cookie-session",
		"jwt-expiry",
		"resource-id-chain",
		"partial-failure",
		"retry-chain",
		"timeout-chain",
	}
	for _, s := range scenarios {
		t.Run(s, func(t *testing.T) {
			bundle, err := OpenBundle(incidentDir(s))
			if err != nil {
				t.Fatalf("OpenBundle(%s): %v", s, err)
			}
			if bundle.InboundLog == "" {
				t.Fatal("InboundLog is empty")
			}
			meta, err := bundle.ReadMetadata()
			if err != nil {
				t.Fatalf("ReadMetadata: %v", err)
			}
			if meta.CapturedAt.IsZero() {
				t.Errorf("CapturedAt is zero")
			}
		})
	}
}

// -----------------------------------------------------------------------
// Inspect scenarios
// -----------------------------------------------------------------------

func TestScenario_Inspect_AuthTokenChain(t *testing.T) {
	bundle, _ := OpenBundle(incidentDir("auth-token-chain"))
	result, err := InspectIncident(bundle.InboundLog)
	if err != nil {
		t.Fatal(err)
	}
	if result.Requests != 2 {
		t.Errorf("expected 2 requests, got %d", result.Requests)
	}
	if result.Tokens == 0 {
		t.Errorf("expected at least 1 token extracted")
	}
	if result.DependencyChains == 0 {
		t.Errorf("expected at least 1 dependency chain (login → orders)")
	}
}

func TestScenario_Inspect_CookieSession(t *testing.T) {
	bundle, _ := OpenBundle(incidentDir("cookie-session"))
	result, err := InspectIncident(bundle.InboundLog)
	if err != nil {
		t.Fatal(err)
	}
	if result.Requests != 2 {
		t.Errorf("expected 2 requests, got %d", result.Requests)
	}
	// Cookie extracted from Set-Cookie header
	if result.Sessions == 0 {
		t.Errorf("expected at least 1 session extracted from Set-Cookie")
	}
}

func TestScenario_Inspect_PartialFailure(t *testing.T) {
	bundle, _ := OpenBundle(incidentDir("partial-failure"))
	result, err := InspectIncident(bundle.InboundLog)
	if err != nil {
		t.Fatal(err)
	}
	if result.Requests != 3 {
		t.Errorf("expected 3 requests, got %d", result.Requests)
	}
	// Check timeline contains the 500 status
	found500 := false
	for _, e := range result.Timeline {
		if e.Status == 500 {
			found500 = true
		}
	}
	if !found500 {
		t.Error("expected 500 status in timeline")
	}
}

func TestScenario_Inspect_RetryChain(t *testing.T) {
	bundle, _ := OpenBundle(incidentDir("retry-chain"))
	result, err := InspectIncident(bundle.InboundLog)
	if err != nil {
		t.Fatal(err)
	}
	if result.Requests != 3 {
		t.Errorf("expected 3 requests (2 retries + 1 success), got %d", result.Requests)
	}
}

func TestScenario_Inspect_TimeoutChain(t *testing.T) {
	bundle, _ := OpenBundle(incidentDir("timeout-chain"))
	result, err := InspectIncident(bundle.InboundLog)
	if err != nil {
		t.Fatal(err)
	}
	if result.Requests != 2 {
		t.Errorf("expected 2 requests (timeout + recovery), got %d", result.Requests)
	}
	// First event should have 504 status
	if len(result.Timeline) > 0 && result.Timeline[0].Status != 504 {
		t.Errorf("expected first request status 504, got %d", result.Timeline[0].Status)
	}
}

// -----------------------------------------------------------------------
// Verify scenarios
// -----------------------------------------------------------------------

func TestScenario_Verify_JWTExpiry(t *testing.T) {
	bundle, _ := OpenBundle(incidentDir("jwt-expiry"))
	result, err := VerifyIncident(bundle.InboundLog)
	if err != nil {
		t.Fatal(err)
	}
	if result.ExpiredTokens == 0 {
		t.Error("expected expired token to be detected")
	}
}

func TestScenario_Verify_RetryChain_SideEffects(t *testing.T) {
	bundle, _ := OpenBundle(incidentDir("retry-chain"))
	result, err := VerifyIncident(bundle.InboundLog)
	if err != nil {
		t.Fatal(err)
	}
	// POST /payments × 3 are side effects
	if result.UnsafeRequests == 0 {
		t.Error("expected POST /payments to be flagged as unsafe")
	}
}

func TestScenario_Verify_AuthTokenChain_ReadinessScore(t *testing.T) {
	bundle, _ := OpenBundle(incidentDir("auth-token-chain"))
	result, err := VerifyIncident(bundle.InboundLog)
	if err != nil {
		t.Fatal(err)
	}
	// Login POST is a side effect (-10), otherwise clean
	if result.ReadinessScore < 50 {
		t.Errorf("expected readiness >= 50, got %d", result.ReadinessScore)
	}
}

func TestScenario_Verify_HealthOnly_FullScore(t *testing.T) {
	// Use the first event only from partial-failure (GET /health → 200)
	// by loading partial-failure (which has 3 GET requests → no side effects)
	bundle, _ := OpenBundle(incidentDir("partial-failure"))
	result, err := VerifyIncident(bundle.InboundLog)
	if err != nil {
		t.Fatal(err)
	}
	// All requests are GET so no side effects expected
	if result.UnsafeRequests != 0 {
		t.Errorf("expected 0 unsafe requests (all GETs), got %d", result.UnsafeRequests)
	}
}

// -----------------------------------------------------------------------
// Deterministic ordering
// -----------------------------------------------------------------------

func TestScenario_LoadEvents_DeterministicOrder(t *testing.T) {
	bundle, _ := OpenBundle(incidentDir("auth-token-chain"))
	events, err := LoadInboundEvents(bundle.InboundLog)
	if err != nil {
		t.Fatal(err)
	}
	// Events should be in timestamp order (login before orders)
	if len(events) < 2 {
		t.Fatal("expected 2 events")
	}
	if !events[0].Timestamp.Before(events[1].Timestamp) && events[0].Sequence >= events[1].Sequence {
		t.Error("events not in deterministic order")
	}
	// Login is first
	if events[0].Method != "POST" {
		t.Errorf("expected POST /login first, got %s %s", events[0].Method, events[0].URL)
	}
}
