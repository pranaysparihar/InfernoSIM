package capture

import (
	"net/http"
	"strings"
	"time"

	"infernosim/pkg/event"
)

// IsGRPCRequest determines whether an HTTP request is gRPC.
func IsGRPCRequest(r *http.Request) bool {
	ct := r.Header.Get("Content-Type")
	return strings.HasPrefix(ct, "application/grpc")
}

// CaptureGRPCInbound captures inbound gRPC calls at the HTTP/2 layer.
func CaptureGRPCInbound(r *http.Request, logger *event.Logger) {
	evt := &event.Event{
		ID:        event.GenerateID(),
		Type:      "InboundGRPC",
		Timestamp: time.Now().UTC(),
		Method:    r.Method,
		URL:       r.URL.Path,
		Headers:   cloneHeaders(r.Header),
	}

	logger.Write(evt)
}

// CaptureGRPCOutbound captures outbound gRPC calls observed via proxying.
func CaptureGRPCOutbound(
	target string,
	status int,
	start time.Time,
	logger *event.Logger,
) {
	evt := &event.Event{
		ID:        event.GenerateID(),
		Type:      "OutboundGRPC",
		Timestamp: start,
		URL:       target,
		Status:    status,
		Duration:  time.Since(start),
	}

	logger.Write(evt)
}