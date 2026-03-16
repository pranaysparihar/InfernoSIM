package replaydriver

import (
	"encoding/json"
	"fmt"
	"infernosim/pkg/event"
	"net/http"
	"strings"
)

type ValueKind string

const (
	ValueKindAuthToken  ValueKind = "auth_token"
	ValueKindCookie     ValueKind = "cookie"
	ValueKindSessionID  ValueKind = "session_id"
	ValueKindResourceID ValueKind = "resource_id"
)

type ProducedValue struct {
	Name      string    `json:"name"`
	Kind      ValueKind `json:"kind"`
	Value     string    `json:"value"`
	Source    string    `json:"source"` // header, body
	Locator   string    `json:"locator"` // key name, json path
}

type ConsumedValue struct {
	Name    string    `json:"name"`
	Kind    ValueKind `json:"kind"`
	Value   string    `json:"value"`
	Target  string    `json:"target"` // header, body, url
	Locator string    `json:"locator"`
}

type DependencyRef struct {
	FromIndex int       `json:"from_index"`
	Target    string    `json:"target"`
	ValueName string    `json:"value_name"`
}

// tokenKeyPatterns is a whitelist of JSON key names that indicate auth tokens.
var tokenKeyPatterns = []string{
	"token", "access_token", "id_token", "refresh_token",
	"jwt", "auth_token", "bearer",
}

// isTokenKey returns true if the JSON key looks like an auth token field.
func isTokenKey(k string) bool {
	lower := strings.ToLower(k)
	for _, pattern := range tokenKeyPatterns {
		if lower == pattern {
			return true
		}
	}
	return false
}

// ExtractResponseValues pulls interesting values from a replayed response
func ExtractResponseValues(resp *http.Response, body []byte) []ProducedValue {
	var produced []ProducedValue

	// 1. Cookies
	for _, c := range resp.Cookies() {
		produced = append(produced, ProducedValue{
			Name:    c.Name,
			Kind:    ValueKindCookie,
			Value:   c.Value,
			Source:  "header",
			Locator: "Set-Cookie",
		})
	}

	// 2. Auth tokens in JSON body
	if strings.Contains(resp.Header.Get("Content-Type"), "application/json") {
		var data map[string]interface{}
		if err := json.Unmarshal(body, &data); err == nil {
			for k, v := range data {
				valStr := fmt.Sprintf("%v", v)
				if isTokenKey(k) {
					produced = append(produced, ProducedValue{
						Name:    k,
						Kind:    ValueKindAuthToken,
						Value:   valStr,
						Source:  "body",
						Locator: k,
					})
				}
				if k == "id" || k == "order_id" {
					produced = append(produced, ProducedValue{
						Name:    k,
						Kind:    ValueKindResourceID,
						Value:   valStr,
						Source:  "body",
						Locator: k,
					})
				}
			}
		}
	}

	return produced
}

// IdentifyConsumers finds where captured values were used in a request
func IdentifyConsumers(e event.Event) []ConsumedValue {
	var consumed []ConsumedValue

	// 1. Authorization header
	if auth := e.Headers["Authorization"]; len(auth) > 0 {
		val := auth[0]
		if strings.HasPrefix(val, "Bearer ") {
			consumed = append(consumed, ConsumedValue{
				Name:    "Authorization",
				Kind:    ValueKindAuthToken,
				Value:   strings.TrimPrefix(val, "Bearer "),
				Target:  "header",
				Locator: "Authorization",
			})
		}
	}

	// 2. Cookies (simplified - captured in headers)
	if cookieHeader := e.Headers["Cookie"]; len(cookieHeader) > 0 {
		for _, c := range strings.Split(cookieHeader[0], ";") {
			parts := strings.SplitN(strings.TrimSpace(c), "=", 2)
			if len(parts) == 2 {
				consumed = append(consumed, ConsumedValue{
					Name:    parts[0],
					Kind:    ValueKindCookie,
					Value:   parts[1],
					Target:  "header",
					Locator: "Cookie",
				})
			}
		}
	}

	return consumed
}
