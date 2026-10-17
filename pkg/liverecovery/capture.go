package liverecovery

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"infernosim/pkg/causal"
	"infernosim/pkg/event"
	"infernosim/pkg/traceorder"
)

type observedWriter struct {
	http.ResponseWriter
	status  int
	body    bytes.Buffer
	bad     bool
	frames  []event.StreamFrame
	last    time.Time
	clock   *traceorder.Clock
	release *traceorder.Stamp
}

func (w *observedWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *observedWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
		// Match the replay controller's release boundary: immediately before
		// starting delivery, with complete delivery validated before sealing.
		w.release = w.clock.Next()
	}
	w.ResponseWriter.WriteHeader(code)
}
func (w *observedWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(200)
	}
	n, err := w.ResponseWriter.Write(b)
	if err != nil || n != len(b) || w.body.Len()+n > MaximumBody || len(w.frames) >= 10000 {
		w.bad = true
	} else {
		w.body.Write(b[:n])
		w.frames = append(w.frames, event.StreamFrame{Delay: time.Since(w.last), BodyB64: base64.StdEncoding.EncodeToString(b[:n]), BodySha256: causal.Digest(b[:n])})
		w.last = time.Now()
	}
	return n, err
}
func (w *observedWriter) Flush() {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	if err := http.NewResponseController(w.ResponseWriter).Flush(); err != nil {
		w.bad = true
	}
}
func (w *observedWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	w.bad = true
	h, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, fmt.Errorf("hijack unsupported")
	}
	return h.Hijack()
}

func (r *Recovery) Observe(w http.ResponseWriter, req *http.Request, body []byte) (http.ResponseWriter, func()) {
	if r == nil {
		return w, func() {}
	}
	admit := r.clock.Next()
	start := time.Now()
	capture := &observedWriter{ResponseWriter: w, last: start, clock: r.clock}
	return capture, func() {
		capture.Flush()
		release := capture.release
		if capture.bad {
			r.captureFailure("incomplete transport response cannot become an offline branch")
			return
		}
		request := append([]byte(nil), body...)
		response := capture.body.Bytes()
		var err error
		stream := strings.Contains(strings.ToLower(capture.Header().Get("Content-Type")), "ndjson")
		if r.options.Privacy != nil {
			request, err = r.options.Privacy.ApplyBody(request)
			if err != nil {
				r.captureFailure("request privacy transform failed")
				return
			}
			if stream {
				var transformed bytes.Buffer
				for _, line := range bytes.Split(bytes.TrimSpace(response), []byte{'\n'}) {
					if len(bytes.TrimSpace(line)) == 0 {
						continue
					}
					v, e := r.options.Privacy.ApplyBody(line)
					if e != nil {
						r.captureFailure("stream privacy transform failed")
						return
					}
					transformed.Write(v)
					transformed.WriteByte('\n')
				}
				response = transformed.Bytes()
			} else {
				response, err = r.options.Privacy.ApplyBody(response)
				if err != nil {
					r.captureFailure("response privacy transform failed")
					return
				}
			}
		}
		u := *req.URL
		if u.Scheme == "" {
			u.Scheme = "http"
		}
		if u.Host == "" {
			u.Host = req.Host
		}
		u.User = nil
		if r.options.Privacy != nil {
			u = *r.options.Privacy.ApplyURL(&u)
		}
		headers := http.Header{}
		for _, name := range []string{"Content-Type", causal.IdentityHeader, "Traceparent"} {
			if v := req.Header.Get(name); v != "" {
				headers.Set(name, v)
			}
		}
		if r.options.Privacy != nil {
			headers = r.options.Privacy.ApplyHeaders(headers)
		}
		e := event.Event{ID: fmt.Sprintf("recovery_%d", admit.Sequence), Type: "OutboundCall", Timestamp: start.UTC(), Method: req.Method, URL: u.String(), Headers: headers, BodySize: int64(len(request)), BodyB64: base64.StdEncoding.EncodeToString(request), BodySha256: causal.Digest(request), Status: capture.status, ResponseCaptured: true, ResponseBodyB64: base64.StdEncoding.EncodeToString(response), ResponseBodySha256: causal.Digest(response), ResponseHeaders: map[string][]string{"Content-Type": {capture.Header().Get("Content-Type")}}, CaptureAdmit: admit, CaptureRelease: release}
		if stream {
			e.ResponseStream = "ndjson"
			if r.options.Privacy == nil {
				e.ResponseFrames = capture.frames
			} else {
				e.ResponseFrames = []event.StreamFrame{{BodyB64: e.ResponseBodyB64, BodySha256: e.ResponseBodySha256}}
			}
		}
		if err = r.logger.Write(&e); err != nil {
			r.captureFailure("continuation capture write failed")
		}
	}
}

func (r *Recovery) captureFailure(message string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.state.CaptureFailure == "" {
		r.state.CaptureFailure = message
	}
}

// Seal is called after the HTTP server has drained. Captured outcomes include
// already-applied faults; the new branch config must not inject them again.
func (r *Recovery) Seal(config []byte, provenance any) error {
	if err := r.Close(); err != nil {
		return err
	}
	for name, value := range map[string]any{"incident.json": map[string]any{"schema_version": 1}, "capture-session.json": r.clock.Snapshot(), "recovery-provenance.json": provenance} {
		b, err := json.MarshalIndent(value, "", "  ")
		if err != nil {
			return err
		}
		if err = os.WriteFile(filepath.Join(r.options.OutputDir, name), append(b, '\n'), 0600); err != nil {
			return err
		}
	}
	if err := os.WriteFile(filepath.Join(r.options.OutputDir, "inbound.log"), nil, 0600); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(r.options.OutputDir, "replay.yaml"), config, 0600)
}
