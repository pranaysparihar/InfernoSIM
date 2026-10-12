package causal

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"infernosim/pkg/event"
)

func HTTPExchange(e event.Event) (Exchange, error) {
	u, err := url.Parse(e.URL)
	if err != nil || u.Hostname() == "" {
		return Exchange{}, fmt.Errorf("event %s has an invalid HTTP URL", e.ID)
	}
	if e.BodyTruncated || e.ResponseBodyTruncated {
		return Exchange{}, fmt.Errorf("event %s is truncated; causal replay requires complete bodies", e.ID)
	}
	if !e.ResponseCaptured || e.Status < 100 || e.Status > 599 {
		return Exchange{}, fmt.Errorf("event %s lacks a complete captured HTTP response", e.ID)
	}
	request, err := base64.StdEncoding.DecodeString(e.BodyB64)
	if err != nil {
		return Exchange{}, err
	}
	if e.BodySize > 0 && len(request) == 0 {
		return Exchange{}, fmt.Errorf("event %s request body was not retained", e.ID)
	}
	response, err := ResponsePayload(e)
	if err != nil {
		return Exchange{}, err
	}
	if e.BodySha256 != "" && e.BodySha256 != Digest(request) {
		return Exchange{}, fmt.Errorf("event %s request digest mismatch", e.ID)
	}
	if e.ResponseBodySha256 != "" && e.ResponseBodySha256 != Digest(response) {
		return Exchange{}, fmt.Errorf("event %s response digest mismatch", e.ID)
	}
	x := Exchange{ID: e.ID, Protocol: "http", Method: e.Method, Host: u.Hostname(), Path: u.RequestURI(), Record: "event:" + e.ID, Request: request, Response: response, Identity: http.Header(e.Headers).Get(IdentityHeader)}
	x.ResponseContentType = http.Header(e.ResponseHeaders).Get("Content-Type")
	x.RecordingHash = Hash(e)
	// W3C traceparent is the only supported HTTP span correlation metadata.
	parts := strings.Split(http.Header(e.Headers).Get("Traceparent"), "-")
	if len(parts) == 4 && len(parts[1]) == 32 && len(parts[2]) == 16 {
		x.TraceID = parts[1]
		x.SpanID = parts[2]
	}
	return x, nil
}

// ResponsePayload validates captured chunks before returning their assembled
// bytes. Chunk boundaries may split a UTF-8 code point or an SSE event.
func ResponsePayload(e event.Event) ([]byte, error) {
	body, err := base64.StdEncoding.DecodeString(e.ResponseBodyB64)
	if err != nil {
		return nil, err
	}
	if len(body) > 16<<20 {
		return nil, fmt.Errorf("captured response exceeds 16 MiB")
	}
	if len(e.ResponseFrames) > 0 {
		if len(e.ResponseFrames) > 10000 {
			return nil, fmt.Errorf("too many captured response chunks")
		}
		var joined bytes.Buffer
		for _, f := range e.ResponseFrames {
			chunk, err := base64.StdEncoding.DecodeString(f.BodyB64)
			if err != nil {
				return nil, err
			}
			if f.BodySha256 != "" && f.BodySha256 != Digest(chunk) {
				return nil, fmt.Errorf("captured response chunk digest mismatch")
			}
			joined.Write(chunk)
			if joined.Len() > 16<<20 {
				return nil, fmt.Errorf("captured response exceeds 16 MiB")
			}
		}
		if len(body) == 0 {
			body = joined.Bytes()
		} else if !bytes.Equal(body, joined.Bytes()) {
			return nil, fmt.Errorf("captured response body and chunks disagree")
		}
	}
	return body, nil
}

// VerifyExchange prevents a graph from being replayed against a different
// recording, including changed outcomes with an unchanged request fingerprint.
func VerifyExchange(g Graph, o Operation, x Exchange) error {
	fp, _, id := RequestFingerprint(x.Protocol, x.Method, x.Host, x.Path, x.Request)
	if fp != o.Fingerprint || x.Record != o.Record || id != o.RequestID {
		return fmt.Errorf("operation %s does not match the recording", o.ID)
	}
	digest := exchangeDigest(x)
	for _, edge := range g.Edges {
		if edge.From == Admit(o.ID) && edge.To == Release(o.ID) {
			for _, e := range edge.Evidence {
				if e.Rule == "request_before_response" && e.Source == x.Record && e.SHA256 == digest {
					return nil
				}
			}
		}
	}
	return fmt.Errorf("operation %s recording evidence digest mismatch", o.ID)
}

func exchangeDigest(x Exchange) string {
	return Hash(struct {
		Request, Response          []byte
		RecordingHash, ContentType string
	}{x.Request, x.Response, x.RecordingHash, x.ResponseContentType})
}
