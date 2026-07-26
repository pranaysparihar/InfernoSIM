package replaydriver

import (
	"encoding/base64"
	"net/http"

	"infernosim/pkg/event"
)

// capturedResponseHeaders returns response headers for both the current paired
// event schema and legacy combined fixtures.
func capturedResponseHeaders(e event.Event) http.Header {
	if e.ResponseCaptured {
		return http.Header(e.ResponseHeaders)
	}
	return http.Header(e.Headers)
}

// capturedResponseBody returns response payload bytes for both the current
// paired event schema and legacy combined fixtures.
func capturedResponseBody(e event.Event) ([]byte, bool) {
	encoded := e.ResponseBodyB64
	if !e.ResponseCaptured {
		encoded = e.BodyB64
	}
	if encoded == "" {
		return nil, false
	}
	body, err := base64.StdEncoding.DecodeString(encoded)
	return body, err == nil
}

func capturedResponseHash(e event.Event) string {
	if e.ResponseCaptured {
		return e.ResponseBodySha256
	}
	return e.BodySha256
}
