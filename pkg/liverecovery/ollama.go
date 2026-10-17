// Package liverecovery implements an explicitly configured, bounded local
// Ollama recovery window. It never provides a tool/network passthrough.
package liverecovery

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"infernosim/pkg/causal"
	"infernosim/pkg/event"
	"infernosim/pkg/privacy"
	"infernosim/pkg/toolstream"
	"infernosim/pkg/traceorder"
)

const MaximumBody = 1 << 20

type Options struct {
	Endpoint, Origin, Model, OutputDir string
	MaxRequests, MaxTokens             int
	Timeout                            time.Duration
	Privacy                            *privacy.Policy
	CaptureSensitiveData               bool
	Review                             string
}

type Snapshot struct {
	Mode                string        `json:"mode"`
	Hybrid              bool          `json:"hybrid"`
	Deterministic       bool          `json:"deterministic"`
	Provider            string        `json:"provider"`
	Model               string        `json:"model"`
	Requests            int           `json:"requests"`
	OutputTokens        int           `json:"output_tokens"`
	MaxRequests         int           `json:"max_requests"`
	MaxTokens           int           `json:"max_output_tokens"`
	Timeout             time.Duration `json:"timeout"`
	Failure             string        `json:"failure,omitempty"`
	CaptureFailure      string        `json:"capture_failure,omitempty"`
	ConfigurationHashes []string      `json:"configuration_hashes,omitempty"`
}

type Recovery struct {
	mu               sync.Mutex
	options          Options
	endpoint, origin *url.URL
	client           *http.Client
	state            Snapshot
	started          time.Time
	busy             bool
	logger           *event.Logger
	clock            *traceorder.Clock
	closeOnce        sync.Once
	closeErr         error
}

func New(o Options) (*Recovery, error) {
	u, err := url.Parse(o.Endpoint)
	if err != nil || u.Scheme != "http" || net.ParseIP(u.Hostname()) == nil || !net.ParseIP(u.Hostname()).IsLoopback() || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, fmt.Errorf("live Ollama endpoint must be an HTTP numeric loopback origin without credentials")
	}
	if o.Origin == "" {
		o.Origin = o.Endpoint
	}
	origin, err := url.Parse(o.Origin)
	if err != nil || origin.Scheme != "http" || origin.Host == "" || origin.User != nil || origin.RawQuery != "" || origin.Fragment != "" || (origin.Path != "" && origin.Path != "/") {
		return nil, fmt.Errorf("live model origin must be an explicit HTTP origin")
	}
	if o.Model == "" || len(o.Model) > 200 || strings.ContainsAny(o.Model, "\r\n") || strings.HasSuffix(o.Model, ":cloud") {
		return nil, fmt.Errorf("explicit local Ollama model required")
	}
	if o.MaxRequests < 1 || o.MaxRequests > 16 || o.MaxTokens < 1 || o.MaxTokens > 8192 || o.Timeout < time.Millisecond || o.Timeout > 10*time.Minute {
		return nil, fmt.Errorf("live budgets require 1..16 requests, 1..8192 output tokens and 1ms..10m timeout")
	}
	if o.OutputDir == "" {
		return nil, fmt.Errorf("live recovery requires a new capture directory")
	}
	if !o.CaptureSensitiveData && (o.Privacy == nil || !o.Privacy.CaptureBodies) {
		return nil, fmt.Errorf("live capture requires an explicit privacy policy with capture_bodies or capture-sensitive-data")
	}
	if err = os.Mkdir(o.OutputDir, 0700); err != nil {
		return nil, err
	}
	logger, err := event.NewLogger(filepath.Join(o.OutputDir, "outbound.log"))
	if err != nil {
		return nil, err
	}
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.Proxy = nil
	return &Recovery{options: o, endpoint: u, origin: origin, logger: logger, clock: traceorder.New(), client: &http.Client{Transport: t, CheckRedirect: func(*http.Request, []*http.Request) error { return fmt.Errorf("live redirects are not allowed") }}, state: Snapshot{Mode: "offline-prefix", Deterministic: true, Provider: "ollama", Model: o.Model, MaxRequests: o.MaxRequests, MaxTokens: o.MaxTokens, Timeout: o.Timeout}}, nil
}

func (r *Recovery) Matches(req *http.Request) bool {
	if r == nil {
		return false
	}
	host := req.URL.Host
	if host == "" {
		host = req.Host
	}
	return req.Method == "POST" && strings.EqualFold(host, r.origin.Host) && req.URL.Path == "/api/chat" && req.URL.RawQuery == "" && (req.URL.Scheme == "" || req.URL.Scheme == r.origin.Scheme)
}
func (r *Recovery) Active() bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.state.Hybrid
}
func (r *Recovery) Snapshot() Snapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.state
	s.ConfigurationHashes = append([]string(nil), s.ConfigurationHashes...)
	return s
}
func (r *Recovery) fail(message string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.state.Failure == "" {
		r.state.Failure = message
	}
	return fmt.Errorf("live recovery: %s", message)
}

// Respond is called only on a divergent model request or after the live branch
// has started. Once active, later matching model calls never rejoin the old tape.
func (r *Recovery) Respond(ctx context.Context, body []byte) (int, http.Header, []byte, error) {
	if len(body) > MaximumBody {
		return 0, nil, nil, r.fail("request exceeds 1 MiB")
	}
	var request map[string]json.RawMessage
	if err := json.Unmarshal(body, &request); err != nil || request == nil {
		return 0, nil, nil, r.fail("malformed model request")
	}
	var model string
	json.Unmarshal(request["model"], &model)
	if model != r.options.Model {
		return 0, nil, nil, r.fail("request model differs from explicitly configured model")
	}
	var messages []json.RawMessage
	if json.Unmarshal(request["messages"], &messages) != nil || len(messages) == 0 || len(messages) > 1000 {
		return 0, nil, nil, r.fail("request requires bounded chat messages")
	}
	stream := true
	if v, ok := request["stream"]; ok {
		if json.Unmarshal(v, &stream) != nil {
			return 0, nil, nil, r.fail("invalid stream flag")
		}
	}
	var options map[string]any
	if v, ok := request["options"]; ok && json.Unmarshal(v, &options) != nil {
		return 0, nil, nil, r.fail("invalid Ollama options")
	}
	if options == nil {
		options = map[string]any{}
	}
	r.mu.Lock()
	if r.state.Failure != "" {
		message := r.state.Failure
		r.mu.Unlock()
		return 0, nil, nil, fmt.Errorf("live recovery: %s", message)
	}
	if r.started.IsZero() {
		r.started = time.Now()
	}
	deadline := r.started.Add(r.options.Timeout)
	if r.busy || r.state.Requests >= r.options.MaxRequests || r.state.OutputTokens >= r.options.MaxTokens || !time.Now().Before(deadline) {
		r.mu.Unlock()
		return 0, nil, nil, r.fail("request/token/time budget exhausted or concurrent live request unsupported")
	}
	remaining := r.options.MaxTokens - r.state.OutputTokens
	r.busy = true
	r.state.Requests++
	r.state.Hybrid = true
	r.state.Deterministic = false
	r.state.Mode = "hybrid-live-recovery"
	options["num_predict"] = remaining
	r.state.ConfigurationHashes = append(r.state.ConfigurationHashes, causal.Hash(options))
	r.mu.Unlock()
	defer func() { r.mu.Lock(); r.busy = false; r.mu.Unlock() }()
	request["options"], _ = json.Marshal(options)
	payload, _ := json.Marshal(request)
	ctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	target := *r.endpoint
	target.Path = "/api/chat"
	upstream, err := http.NewRequestWithContext(ctx, "POST", target.String(), bytes.NewReader(payload))
	// URL construction never uses a caller-controlled destination or headers.
	if err != nil {
		return 0, nil, nil, r.fail("invalid provider request")
	}
	upstream.URL.Path = "/api/chat"
	upstream.URL.Host = r.endpoint.Host
	upstream.URL.Scheme = "http"
	upstream.Header.Set("Content-Type", "application/json")
	response, err := r.client.Do(upstream)
	if err != nil {
		return 0, nil, nil, r.fail("provider request failed or was cancelled")
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return 0, nil, nil, r.fail("provider returned non-200 response")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, MaximumBody+1))
	if err != nil || len(data) > MaximumBody {
		return 0, nil, nil, r.fail("incomplete or oversized provider response")
	}
	tokens, err := validateResponse(data, stream, r.options.Model)
	if err != nil {
		return 0, nil, nil, r.fail(err.Error())
	}
	r.mu.Lock()
	r.state.OutputTokens += tokens
	over := r.state.OutputTokens > r.options.MaxTokens
	r.mu.Unlock()
	if over {
		return 0, nil, nil, r.fail("provider exceeded output token budget")
	}
	if ctx.Err() != nil {
		return 0, nil, nil, r.fail("recovery window expired")
	}
	headers := http.Header{"Content-Type": []string{"application/json"}}
	if stream {
		headers.Set("Content-Type", "application/x-ndjson")
	}
	return 200, headers, data, nil
}

func validateResponse(data []byte, stream bool, model string) (int, error) {
	frames := [][]byte{data}
	if stream {
		if _, err := toolstream.Parse("ollama", data); err != nil {
			return 0, fmt.Errorf("malformed or incomplete Ollama stream")
		}
		frames = bytes.Split(bytes.TrimSpace(data), []byte{'\n'})
	}
	var terminal map[string]json.RawMessage
	for _, frame := range frames {
		if len(bytes.TrimSpace(frame)) == 0 {
			continue
		}
		var v map[string]json.RawMessage
		if json.Unmarshal(frame, &v) != nil {
			return 0, fmt.Errorf("malformed provider JSON")
		}
		if _, ok := v["error"]; ok {
			return 0, fmt.Errorf("provider error envelope")
		}
		var gotModel string
		json.Unmarshal(v["model"], &gotModel)
		if gotModel != model {
			return 0, fmt.Errorf("provider model provenance mismatch")
		}
		var done bool
		if json.Unmarshal(v["done"], &done) != nil {
			return 0, fmt.Errorf("provider completion flag missing")
		}
		if terminal != nil {
			return 0, fmt.Errorf("data after provider completion")
		}
		if done {
			terminal = v
		}
	}
	if terminal == nil {
		return 0, fmt.Errorf("provider response incomplete")
	}
	var count int
	if json.Unmarshal(terminal["eval_count"], &count) != nil || count < 0 {
		return 0, fmt.Errorf("provider output usage missing or invalid")
	}
	if !stream {
		if _, err := toolstream.Parse("ollama", append(append([]byte(nil), bytes.TrimSpace(data)...), '\n')); err != nil {
			return 0, fmt.Errorf("unsupported Ollama response envelope")
		}
	}
	return count, nil
}

func (r *Recovery) Close() error {
	r.closeOnce.Do(func() { r.client.CloseIdleConnections(); r.closeErr = r.logger.Close() })
	return r.closeErr
}
