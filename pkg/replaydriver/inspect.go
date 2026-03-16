package replaydriver

import (
	"encoding/base64"
	"infernosim/pkg/event"
	"net/http"
	"net/url"
	"time"
)

// InspectResult summarises the dependency structure of a captured incident.
type InspectResult struct {
	Requests         int
	DependencyChains int
	Tokens           int
	Sessions         int
	ResourceIDs      int
	Timeline         []TimelineEntry
}

// TimelineEntry describes a single request in the incident timeline.
type TimelineEntry struct {
	Index     int
	Method    string
	URL       string
	Status    int
	Duration  time.Duration
	Timestamp time.Time
	Produces  []ProducedValue
	Consumes  []ConsumedValue
}

// InspectIncident loads and analyses the captured events in inboundLog,
// returning a summary of requests, dependency chains, and a request timeline.
func InspectIncident(inboundLog string) (InspectResult, error) {
	events, err := LoadInboundEvents(inboundLog)
	if err != nil {
		return InspectResult{}, err
	}

	var timeline []TimelineEntry
	tokenSet := make(map[string]struct{})
	sessionSet := make(map[string]struct{})
	resourceIDSet := make(map[string]struct{})

	// Track produced VALUES across all prior events (for chain detection).
	producedValues := make(map[string]struct{})
	chainCount := 0

	for i, e := range events {
		consumed := IdentifyConsumers(e)

		// A dependency chain exists when a consumed value was produced by a prior event.
		for _, c := range consumed {
			if _, ok := producedValues[c.Value]; ok {
				chainCount++
				break
			}
		}

		// Extract produced values from the captured response body + headers.
		var produced []ProducedValue
		if e.BodyB64 != "" {
			if body, err := base64.StdEncoding.DecodeString(e.BodyB64); err == nil {
				// Copy all event headers to fakeResp so cookies and Content-Type are available.
				fakeResp := &http.Response{Header: make(http.Header)}
				for k, vals := range e.Headers {
					for _, v := range vals {
						fakeResp.Header.Add(k, v)
					}
				}
				produced = ExtractResponseValues(fakeResp, body)
			}
		}

		// Count unique value kinds and register produced values for future chain detection.
		for _, p := range produced {
			switch p.Kind {
			case ValueKindAuthToken:
				tokenSet[p.Value] = struct{}{}
			case ValueKindCookie, ValueKindSessionID:
				sessionSet[p.Value] = struct{}{}
			case ValueKindResourceID:
				resourceIDSet[p.Value] = struct{}{}
			}
			producedValues[p.Value] = struct{}{}
		}

		// Shorten URL to path only for readability.
		displayURL := e.URL
		if parsed, err := url.Parse(e.URL); err == nil {
			displayURL = parsed.Path
			if parsed.RawQuery != "" {
				displayURL += "?" + parsed.RawQuery
			}
		}

		timeline = append(timeline, TimelineEntry{
			Index:     i + 1,
			Method:    e.Method,
			URL:       displayURL,
			Status:    e.Status,
			Duration:  e.Duration,
			Timestamp: e.Timestamp,
			Produces:  produced,
			Consumes:  consumed,
		})
	}

	return InspectResult{
		Requests:         len(events),
		DependencyChains: chainCount,
		Tokens:           len(tokenSet),
		Sessions:         len(sessionSet),
		ResourceIDs:      len(resourceIDSet),
		Timeline:         timeline,
	}, nil
}

// PrintInspectResult writes a human-friendly inspection summary to stdout.
func PrintInspectResult(r InspectResult) {
	pad := func(s string, w int) string {
		for len(s) < w {
			s += " "
		}
		return s
	}
	println := func(s string) { print(s + "\n") }

	println("Incident Summary")
	println("----------------")
	println("Requests:          " + itoa(r.Requests))
	println("Dependency chains: " + itoa(r.DependencyChains))
	println("Tokens:            " + itoa(r.Tokens))
	println("Sessions:          " + itoa(r.Sessions))
	println("Resource IDs:      " + itoa(r.ResourceIDs))
	println("")
	println("Request Timeline:")

	for _, e := range r.Timeline {
		idx := pad(itoa(e.Index), 4)
		method := pad(e.Method, 7)
		status := itoa(e.Status)
		dur := e.Duration.Round(time.Millisecond).String()
		line := "  #" + idx + method + pad(e.URL, 40) + pad(status, 6) + pad(dur, 10)

		var annotations []string
		for _, p := range e.Produces {
			annotations = append(annotations, "→ produces: "+p.Name)
		}
		for _, c := range e.Consumes {
			annotations = append(annotations, "← consumes: "+c.Name)
		}
		for _, a := range annotations {
			line += " " + a
		}
		println(line)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	buf := [20]byte{}
	pos := len(buf)
	for n > 0 {
		pos--
		buf[pos] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}

// keep event import used
var _ event.Event
