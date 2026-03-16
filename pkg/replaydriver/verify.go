package replaydriver

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// VerifyResult holds the safety assessment of a captured incident.
type VerifyResult struct {
	ReadinessScore int // 0–100
	UnsafeRequests int
	MissingDeps    int
	ExpiredTokens  int
	Issues         []VerifyIssue
}

// VerifyIssue describes a single safety concern in the incident.
type VerifyIssue struct {
	RequestIndex int
	Severity     string // "error" | "warning"
	Kind         string // "side_effect" | "missing_dep" | "expired_token"
	Detail       string
}

// VerifyIncident analyses the captured events in inboundLog and returns
// a safety assessment with a readiness score and a list of issues.
func VerifyIncident(inboundLog string) (VerifyResult, error) {
	events, err := LoadInboundEvents(inboundLog)
	if err != nil {
		return VerifyResult{}, err
	}

	var issues []VerifyIssue

	// Build a set of locators produced by prior requests (for missing-dep detection).
	producedLocators := make(map[string]struct{})

	for i, e := range events {
		idx := i + 1

		// --- Side-effect detection ---
		if isSideEffect(e.Method) {
			issues = append(issues, VerifyIssue{
				RequestIndex: idx,
				Severity:     "warning",
				Kind:         "side_effect",
				Detail:       fmt.Sprintf("%s %s: non-idempotent write", e.Method, shortPath(e.URL)),
			})
		}

		// --- Missing dependency detection ---
		consumed := IdentifyConsumers(e)
		for _, c := range consumed {
			if _, ok := producedLocators[c.Locator]; !ok {
				issues = append(issues, VerifyIssue{
					RequestIndex: idx,
					Severity:     "error",
					Kind:         "missing_dep",
					Detail:       fmt.Sprintf("%s %s: %s value not found in prior captured responses", e.Method, shortPath(e.URL), c.Name),
				})
			}
		}

		// --- Expired token detection ---
		if auth := e.Headers["Authorization"]; len(auth) > 0 {
			val := auth[0]
			if strings.HasPrefix(val, "Bearer ") {
				token := strings.TrimPrefix(val, "Bearer ")
				if exp, ok := jwtExpiry(token); ok {
					if e.Timestamp.After(exp) {
						issues = append(issues, VerifyIssue{
							RequestIndex: idx,
							Severity:     "error",
							Kind:         "expired_token",
							Detail:       fmt.Sprintf("%s %s: Bearer token expired at %s (captured at %s)", e.Method, shortPath(e.URL), exp.Format(time.RFC3339), e.Timestamp.Format(time.RFC3339)),
						})
					}
				}
			}
		}

		// Update produced locators from this event's response body.
		if e.BodyB64 != "" {
			if body, err := base64.StdEncoding.DecodeString(e.BodyB64); err == nil {
				produced := extractBodyProducedLocators(body, e)
				for loc := range produced {
					producedLocators[loc] = struct{}{}
				}
			}
		}
	}

	// Count by kind.
	unsafe, missing, expired := 0, 0, 0
	for _, iss := range issues {
		switch iss.Kind {
		case "side_effect":
			unsafe++
		case "missing_dep":
			missing++
		case "expired_token":
			expired++
		}
	}

	score := 100 - (unsafe*10 + missing*15 + expired*20)
	if score < 0 {
		score = 0
	}

	return VerifyResult{
		ReadinessScore: score,
		UnsafeRequests: unsafe,
		MissingDeps:    missing,
		ExpiredTokens:  expired,
		Issues:         issues,
	}, nil
}

// PrintVerifyResult writes a human-friendly verification report to stdout.
func PrintVerifyResult(r VerifyResult) {
	fmt.Println("Incident Verification")
	fmt.Println("---------------------")
	fmt.Printf("Replay readiness:     %d%%\n", r.ReadinessScore)
	fmt.Printf("Unsafe requests:      %d\n", r.UnsafeRequests)
	fmt.Printf("Missing dependencies: %d\n", r.MissingDeps)
	fmt.Printf("Expired tokens:       %d\n", r.ExpiredTokens)

	if len(r.Issues) > 0 {
		fmt.Println("\nIssues:")
		for _, iss := range r.Issues {
			sev := strings.ToUpper(iss.Severity)
			fmt.Printf("  [%s] Request #%d  %s\n", sev, iss.RequestIndex, iss.Detail)
		}
	}
}

// jwtExpiry decodes the exp claim from a JWT without verifying its signature.
// Returns (expiry, true) if found and parseable.
func jwtExpiry(token string) (time.Time, bool) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return time.Time{}, false
	}
	payload := parts[1]
	// Base64 padding
	switch len(payload) % 4 {
	case 2:
		payload += "=="
	case 3:
		payload += "="
	}
	data, err := base64.URLEncoding.DecodeString(payload)
	if err != nil {
		// Try RawURLEncoding (no padding)
		data, err = base64.RawURLEncoding.DecodeString(parts[1])
		if err != nil {
			return time.Time{}, false
		}
	}
	var claims map[string]interface{}
	if err := json.Unmarshal(data, &claims); err != nil {
		return time.Time{}, false
	}
	expRaw, ok := claims["exp"]
	if !ok {
		return time.Time{}, false
	}
	expFloat, ok := expRaw.(float64)
	if !ok {
		return time.Time{}, false
	}
	return time.Unix(int64(expFloat), 0), true
}

// extractBodyProducedLocators returns the set of locators present in a JSON response body.
func extractBodyProducedLocators(body []byte, e interface{}) map[string]struct{} {
	result := make(map[string]struct{})
	var data map[string]interface{}
	if err := json.Unmarshal(body, &data); err != nil {
		return result
	}
	for k := range data {
		result[k] = struct{}{}
	}
	return result
}

// shortPath returns just the path component of a URL, or the full string on parse error.
func shortPath(rawURL string) string {
	if idx := strings.Index(rawURL, "?"); idx >= 0 {
		rawURL = rawURL[:idx]
	}
	if idx := strings.Index(rawURL, "//"); idx >= 0 {
		rawURL = rawURL[idx+2:]
		if idx2 := strings.Index(rawURL, "/"); idx2 >= 0 {
			return rawURL[idx2:]
		}
	}
	return rawURL
}
