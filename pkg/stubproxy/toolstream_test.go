package stubproxy

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"infernosim/pkg/agentreliability"
	"infernosim/pkg/causal"
	"infernosim/pkg/event"
	"infernosim/pkg/toolstream"
)

func TestCausalHTTPRuntimeIDRemappingUpdatesLengthAndChunks(t *testing.T) {
	for _, framed := range []bool{false, true} {
		request := []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"lookup"}}`)
		body := []byte(`{"jsonrpc":"2.0","id":1,"result":{"ok":true}}`)
		e := event.Event{ID: "call", Type: "OutboundCall", Method: "POST", URL: "http://mcp.test/tools", BodyB64: base64.StdEncoding.EncodeToString(request), Status: 200, ResponseCaptured: true,
			ResponseHeaders: http.Header{"Content-Type": {"application/json"}, "Content-Length": {strconv.Itoa(len(body))}}, ResponseBodyB64: base64.StdEncoding.EncodeToString(body)}
		if framed {
			e.ResponseFrames = chunks(body, 0)
		}
		x, err := causal.HTTPExchange(e)
		if err != nil {
			t.Fatal(err)
		}
		g, err := causal.Discover([]causal.Exchange{x}, nil, causal.Declarations{Review: "fixture"})
		if err != nil {
			t.Fatal(err)
		}
		p, err := causal.Generate(g, 0, 1)
		if err != nil {
			t.Fatal(err)
		}
		engine, err := agentreliability.NewEngine(agentreliability.Config{Enabled: true, Adapters: agentreliability.Adapters{MCP: true}, Causal: &causal.Replay{Graph: g, Schedule: p.Schedules[0], Timeout: "1s"}}, nil)
		if err != nil {
			t.Fatal(err)
		}
		stub, err := NewWithOptions(writeOutboundFixture(t, e), "", nil, Options{Agent: engine})
		if err != nil {
			t.Fatal(err)
		}
		server := httptest.NewServer(stub.Handler())
		req, err := http.NewRequest("POST", server.URL+"/tools", bytes.NewReader(bytes.Replace(request, []byte(`"id":1`), []byte(`"id":"runtime-id-with-different-length"`), 1)))
		if err != nil {
			t.Fatal(err)
		}
		req.Host = "mcp.test"
		resp, err := server.Client().Do(req)
		if err != nil {
			server.Close()
			t.Fatal(err)
		}
		actual, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		server.Close()
		if err != nil {
			t.Fatal(err)
		}
		var value map[string]any
		if err := json.Unmarshal(actual, &value); err != nil {
			t.Fatalf("truncated remapped body: %s: %v", actual, err)
		}
		if value["id"] != "runtime-id-with-different-length" {
			t.Fatalf("old ID delivered: %s", actual)
		}
		if err := engine.Causal().Complete(); err != nil {
			t.Fatal(err)
		}
	}
}

func chunks(body []byte, delay time.Duration) []event.StreamFrame {
	var frames []event.StreamFrame
	for len(body) > 0 {
		n := min(73, len(body)) // Deliberately split JSON strings and SSE events.
		frames = append(frames, event.StreamFrame{BodyB64: base64.StdEncoding.EncodeToString(body[:n]), BodySha256: causal.Digest(body[:n]), Delay: delay})
		body = body[n:]
	}
	return frames
}

func TestFragmentedProviderStreamOverHTTP(t *testing.T) {
	for _, scheduled := range []bool{false, true} {
		for file, provider := range map[string]string{"responses.sse": "openai", "chat.sse": "openai", "anthropic.sse": "anthropic", "ollama.ndjson": "ollama"} {
			t.Run(file+map[bool]string{false: "-legacy", true: "-causal"}[scheduled], func(t *testing.T) {
				body, err := os.ReadFile(filepath.Join("..", "toolstream", "testdata", file))
				if err != nil {
					t.Fatal(err)
				}
				contentType, id := "text/event-stream", "call_b"
				if provider == "ollama" {
					contentType, id = "application/x-ndjson", "ollama:1"
				}
				e := event.Event{ID: "response", Type: "OutboundCall", Method: "POST", URL: "http://llm.test/model", BodyB64: base64.StdEncoding.EncodeToString([]byte(`{}`)), Status: 200,
					ResponseCaptured: true, ResponseHeaders: http.Header{"Content-Type": {contentType}}, ResponseTrailers: http.Header{"X-Finished": {"yes"}}, ResponseFrames: chunks(body, time.Millisecond)}
				cfg := agentreliability.Config{Enabled: true, Adapters: agentreliability.Adapters{Providers: []agentreliability.ProviderAdapter{{Name: "model", Type: provider, HostRegex: "^llm.test$"}}},
					Faults: []agentreliability.Fault{{ID: "amount", Select: agentreliability.Selector{Kind: "llm_response", Provider: "model", Tool: "lookup", CallID: id}, Mutations: []agentreliability.Mutation{{Target: "tool_arguments", Operation: "set", Path: "$.amount", Value: 99}}}}}
				if scheduled {
					x, err := causal.HTTPExchange(e)
					if err != nil {
						t.Fatal(err)
					}
					g, err := causal.Discover([]causal.Exchange{x}, nil, causal.Declarations{Review: "stream parser fixture"})
					if err != nil {
						t.Fatal(err)
					}
					// The proposed tools are not executed in this parser fixture.
					for i := range g.Issues {
						g.Issues[i].Resolution = "only model response is consumed by this test"
					}
					p, err := causal.Generate(g, 42, 1)
					if err != nil {
						t.Fatal(err)
					}
					cfg.Causal = &causal.Replay{Graph: g, Schedule: p.Schedules[0], Timeout: "2s"}
				}
				engine, err := agentreliability.NewEngine(cfg, []string{"amount"})
				if err != nil {
					t.Fatal(err)
				}
				stub, err := NewWithOptions(writeOutboundFixture(t, e), "", nil, Options{Agent: engine})
				if err != nil {
					t.Fatal(err)
				}
				server := httptest.NewServer(stub.Handler())
				defer server.Close()
				req, err := http.NewRequest("POST", server.URL+"/model", bytes.NewBufferString(`{}`))
				if err != nil {
					t.Fatal(err)
				}
				req.Host = "llm.test"
				response, err := server.Client().Do(req)
				if err != nil {
					t.Fatal(err)
				}
				actual, err := io.ReadAll(response.Body)
				response.Body.Close()
				if err != nil {
					t.Fatal(err)
				}
				if response.StatusCode != 200 {
					t.Fatalf("status %d: %s", response.StatusCode, actual)
				}
				parsed, err := toolstream.Parse(provider, actual)
				if err != nil {
					t.Fatal(err)
				}
				original, err := toolstream.Parse(provider, body)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(parsed.Calls[0].Arguments, original.Calls[0].Arguments) || !bytes.Contains(parsed.Calls[1].Arguments, []byte(`99`)) {
					t.Fatal("wrong parallel call mutated")
				}
				if response.Trailer.Get("X-Finished") != "yes" {
					t.Fatal("stream trailer lost")
				}
				if scheduled {
					if err := engine.Causal().Complete(); err != nil {
						t.Fatal(err)
					}
				}
			})
		}
	}
}

type cancelAfterFlush struct {
	*httptest.ResponseRecorder
	cancel  context.CancelFunc
	flushes int
}

func (w *cancelAfterFlush) Flush() { w.ResponseRecorder.Flush(); w.flushes++; w.cancel() }

func TestCapturedStreamCancellationAndReframing(t *testing.T) {
	frames := chunks([]byte("first"+string(bytes.Repeat([]byte("x"), 160))), 0)
	frames[1].Delay = time.Minute
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := &cancelAfterFlush{ResponseRecorder: httptest.NewRecorder(), cancel: cancel}
	start := time.Now()
	err := writeCapturedResponseFrames(ctx, w, 200, http.Header{}, nil, frames)
	if err != context.Canceled || w.flushes != 1 || w.Body.Len() != 73 || time.Since(start) > time.Second {
		t.Fatalf("cancellation ignored: %v, flushes %d", err, w.flushes)
	}
	for _, body := range [][]byte{[]byte("short"), bytes.Repeat([]byte("long"), 200)} {
		changed, err := reframeResponse(frames, body)
		if err != nil {
			t.Fatal(err)
		}
		if len(changed) != len(frames) || changed[1].Delay != frames[1].Delay {
			t.Fatal("chunk timing contract changed")
		}
		var actual []byte
		for _, frame := range changed {
			b, _ := base64.StdEncoding.DecodeString(frame.BodyB64)
			actual = append(actual, b...)
			if causal.Digest(b) != frame.BodySha256 {
				t.Fatal("bad digest")
			}
		}
		if !bytes.Equal(actual, body) {
			t.Fatal("reframing changed bytes")
		}
	}
}
