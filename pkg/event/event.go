package event

import (
	"crypto/rand"
	"encoding/hex"
	"time"
)

// Event represents a single captured execution event
type Event struct {
	ID        string        `json:"id"`
	Type      string        `json:"type"`
	Timestamp time.Time     `json:"timestamp"`

	Service  string        `json:"service,omitempty"`
	Method   string        `json:"method,omitempty"`
	URL      string        `json:"url,omitempty"`
	Status   int           `json:"status,omitempty"`
	Duration time.Duration `json:"duration,omitempty"`
	Error    string        `json:"error,omitempty"`

	Headers map[string][]string `json:"headers,omitempty"`
	BodySize int64              `json:"bodySize,omitempty"`
	TraceID  string             `json:"traceId,omitempty"`

	// Payload tracking
	BodyB64       string `json:"bodyB64,omitempty"`
	BodySha256    string `json:"bodySha256,omitempty"`
	BodyTruncated bool   `json:"bodyTruncated,omitempty"`
	BytesSent     int64  `json:"bytesSent,omitempty"`
	BytesReceived int64  `json:"bytesReceived,omitempty"`

	// gRPC specific
	GrpcServiceMethod string `json:"grpcServiceMethod,omitempty"`
	GrpcStatus        string `json:"grpcStatus,omitempty"`

	// Fault injection flag (from pkg/inject)
	InjectionApplied string `json:"injectionApplied,omitempty"`
}

// GenerateID returns a random hex string ID
func GenerateID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// fallback: timestamp encoded as hex string
		return hex.EncodeToString([]byte(
			time.Now().Format(time.RFC3339Nano),
		))
	}
	return hex.EncodeToString(b)
}