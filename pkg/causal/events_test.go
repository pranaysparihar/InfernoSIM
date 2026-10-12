package causal

import (
	"net/http"
	"testing"

	"infernosim/pkg/event"
)

func TestHTTPRecordingEvidenceBindsTransportMetadata(t *testing.T) {
	e := event.Event{ID: "request", Type: "OutboundCall", Method: "GET", URL: "http://test/value", Status: 200, ResponseCaptured: true}
	x, err := HTTPExchange(e)
	if err != nil {
		t.Fatal(err)
	}
	g, err := Discover([]Exchange{x}, nil, Declarations{Review: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyExchange(g, g.Operations[0], x); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*event.Event){func(e *event.Event) { e.Status = 500 }, func(e *event.Event) { e.ResponseHeaders = http.Header{"Content-Type": {"text/plain"}} }, func(e *event.Event) { e.ResponseTrailers = http.Header{"X-Result": {"changed"}} }} {
		other := e
		change(&other)
		changed, err := HTTPExchange(other)
		if err != nil {
			t.Fatal(err)
		}
		if err := VerifyExchange(g, g.Operations[0], changed); err == nil {
			t.Fatal("changed recording accepted")
		}
	}
}

func TestStreamedPreviousResponseDependency(t *testing.T) {
	stream := []byte("data: {\"type\":\"response.created\",\"response\":{\"id\":\"response1\"}}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"response1\",\"status\":\"completed\",\"output\":[]}}\n\n")
	g, err := Discover([]Exchange{{ID: "one", Protocol: "http", Record: "one", Response: stream, ResponseContentType: "text/event-stream"}, {ID: "two", Protocol: "http", Record: "two", Request: []byte(`{"previous_response_id":"response1"}`)}}, nil, Declarations{Review: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range g.Edges {
		if e.From == Release("one") && e.To == Admit("two") {
			found = true
		}
	}
	if !found {
		t.Fatal("streamed response identity did not create dependency")
	}
}

func TestMissingHTTPCaptureIsNotAnEmptySuccessfulResponse(t *testing.T) {
	base := event.Event{ID: "x", Method: "GET", URL: "http://test/x", Status: 200, ResponseCaptured: true}
	for _, change := range []func(*event.Event){func(e *event.Event) { e.ResponseCaptured = false }, func(e *event.Event) { e.BodyTruncated = true }, func(e *event.Event) { e.BodySize = 42 }, func(e *event.Event) { e.Status = 0 }} {
		e := base
		change(&e)
		if _, err := HTTPExchange(e); err == nil {
			t.Fatal("incomplete capture accepted")
		}
	}
}
