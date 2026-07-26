package event

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"regexp"
	"time"
)

// Event represents a single captured execution event
type Event struct {
	ID        string    `json:"id"`
	Type      string    `json:"type"`
	Timestamp time.Time `json:"timestamp"`
	Sequence  int64     `json:"sequence,omitempty"` // monotonic counter for deterministic ordering

	Service  string        `json:"service,omitempty"`
	Method   string        `json:"method,omitempty"`
	URL      string        `json:"url,omitempty"`
	Status   int           `json:"status,omitempty"`
	Duration time.Duration `json:"duration,omitempty"`
	Error    string        `json:"error,omitempty"`

	Headers  map[string][]string `json:"headers,omitempty"`
	BodySize int64               `json:"bodySize,omitempty"`
	TraceID  string              `json:"traceId,omitempty"`

	// Payload tracking
	BodyB64       string `json:"bodyB64,omitempty"`
	BodySha256    string `json:"bodySha256,omitempty"`
	BodyTruncated bool   `json:"bodyTruncated,omitempty"`
	BodyRedacted  bool   `json:"bodyRedacted,omitempty"`
	BytesSent     int64  `json:"bytesSent,omitempty"`
	BytesReceived int64  `json:"bytesReceived,omitempty"`

	// Captured response data. InboundResponse events continue to use the
	// primary Headers/Body fields; these fields are populated when a request
	// and response are correlated or when an outbound dependency call is
	// captured as a single exchange.
	ResponseHeaders       map[string][]string `json:"responseHeaders,omitempty"`
	ResponseTrailers      map[string][]string `json:"responseTrailers,omitempty"`
	ResponseBodyB64       string              `json:"responseBodyB64,omitempty"`
	ResponseBodySha256    string              `json:"responseBodySha256,omitempty"`
	ResponseBodyTruncated bool                `json:"responseBodyTruncated,omitempty"`
	ResponseBodyRedacted  bool                `json:"responseBodyRedacted,omitempty"`
	ResponseCaptured      bool                `json:"responseCaptured,omitempty"`

	// gRPC specific
	GrpcServiceMethod string `json:"grpcServiceMethod,omitempty"`
	GrpcStatus        string `json:"grpcStatus,omitempty"`

	// Fault injection flag (from pkg/inject)
	InjectionApplied string `json:"injectionApplied,omitempty"`
}

// GenerateID returns a cryptographically random 32-character hex string.
// It panics if the OS CSPRNG is unavailable — a fatal environment misconfiguration
// that must not be silently downgraded to a weak fallback.
func GenerateID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("infernosim: crypto/rand unavailable: %v", err))
	}
	return hex.EncodeToString(b)
}

// traceIDRe matches only the 32-character lowercase hex IDs produced by GenerateID.
var traceIDRe = regexp.MustCompile(`^[0-9a-f]{32}$`)

// SanitizeTraceID returns raw if it looks like a valid internal trace ID,
// or an empty string otherwise. This prevents external callers from injecting
// arbitrary values (including JSON control characters) into the event log
// by supplying a crafted X-Inferno-TraceID header.
func SanitizeTraceID(raw string) string {
	if traceIDRe.MatchString(raw) {
		return raw
	}
	return ""
}
