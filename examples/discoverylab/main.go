// discoverylab demonstrates a real local outbox bug hidden by a correctly
// deduplicating simulated payment service. It never contacts a payment provider.
package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"infernosim/pkg/event"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"
)

const replay = `agent:
  enabled: true
  adapters:
    mcp: true
  limits:
    max_cases: 100
    max_calls: 10
  effects:
    - name: refund
      select: {tool: refund}
      identity: [$.arguments.payment_id]
      deduplicate_by: [$.arguments.idempotency_key]
  faults:
    - id: response-lost
      select: {tool: refund, occurrence: 1}
      committed_response_lost: true
    - id: extra-header
      select: {tool: refund, occurrence: 1}
      headers: {X-Synthetic-Noise: yes}
    - id: another-header
      select: {tool: refund, occurrence: 1}
      headers: {X-Other-Noise: yes}
  assertions:
    - {id: remote-refund-once, type: exactly_once_effect, effect: refund}
    - {id: recorded-only, type: no_unexpected_calls}
`

func main() {
	initDir := flag.String("init", "", "Create synthetic incident")
	setup := flag.Bool("setup", false, "Reset application outbox")
	check := flag.Bool("check", false, "Independently inspect the application outbox")
	mode := flag.String("mode", "safe", "safe or unsafe application")
	flag.Parse()
	path := filepath.Join(os.Getenv("INFERNOSIM_STATE_DIR"), "outbox.json")
	var err error
	switch {
	case *initDir != "":
		err = initialize(*initDir)
	case *setup:
		err = os.WriteFile(path, []byte("[]"), 0o600)
	case *check:
		data, e := os.ReadFile(path)
		var entries []string
		valid := e == nil && json.Unmarshal(data, &entries) == nil
		if !valid {
			err = fmt.Errorf("outbox unavailable")
			break
		}
		err = json.NewEncoder(os.Stdout).Encode(map[string]any{"assertions": []any{map[string]any{"id": "outbox-once", "passed": len(entries) == 1 && entries[0] == "synthetic-payment"}}})
	default:
		err = run(path, *mode)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func initialize(dir string) error {
	if err := os.Mkdir(dir, 0o700); err != nil {
		return err
	}
	var log bytes.Buffer
	for i, tool := range []string{"refund", "refund_status"} {
		request := []byte(`{"jsonrpc":"2.0","id":"fixture","method":"tools/call","params":{"name":"` + tool + `","arguments":{"payment_id":"synthetic-payment","idempotency_key":"refund-1"}}}`)
		response := []byte(`{"jsonrpc":"2.0","id":"fixture","result":{"status":"refunded"}}`)
		e := event.Event{ID: tool, Type: "OutboundCall", Sequence: int64(i + 1), Method: "POST", URL: "http://mcp.test/mcp", BodyB64: base64.StdEncoding.EncodeToString(request), Status: 200, ResponseCaptured: true, ResponseHeaders: http.Header{"Content-Type": {"application/json"}}, ResponseBodyB64: base64.StdEncoding.EncodeToString(response)}
		if err := json.NewEncoder(&log).Encode(e); err != nil {
			return err
		}
	}
	for name, data := range map[string][]byte{"replay.yaml": []byte(replay), "inbound.log": {}, "outbound.log": log.Bytes()} {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			return err
		}
	}
	return nil
}
func run(path, mode string) error {
	if mode != "safe" && mode != "unsafe" {
		return fmt.Errorf("mode must be safe or unsafe")
	}
	proxy, err := url.Parse(os.Getenv("INFERNOSIM_PROXY_URL"))
	if err != nil || proxy.Host == "" {
		return fmt.Errorf("run through InfernoSIM")
	}
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxy), DisableKeepAlives: true}, Timeout: 2 * time.Second}
	defer client.CloseIdleConnections()
	appendOutbox := func() error {
		b, e := os.ReadFile(path)
		if e != nil {
			return e
		}
		var rows []string
		if e = json.Unmarshal(b, &rows); e != nil {
			return e
		}
		rows = append(rows, "synthetic-payment")
		b, e = json.Marshal(rows)
		if e != nil {
			return e
		}
		return os.WriteFile(path, b, 0o600)
	}
	call := func(tool string) error {
		body := `{"jsonrpc":"2.0","id":"call","method":"tools/call","params":{"name":"` + tool + `","arguments":{"payment_id":"synthetic-payment","idempotency_key":"refund-1"}}}`
		response, e := client.Post("http://mcp.test/mcp", "application/json", bytes.NewBufferString(body))
		if e != nil {
			return e
		}
		defer response.Body.Close()
		_, e = io.Copy(io.Discard, response.Body)
		if e != nil {
			return e
		}
		if response.StatusCode != 200 {
			return fmt.Errorf("tool failed")
		}
		return nil
	}
	if err = appendOutbox(); err != nil {
		return err
	}
	if err = call("refund"); err != nil {
		if mode == "unsafe" {
			if err = appendOutbox(); err != nil {
				return err
			}
			return call("refund")
		}
		return call("refund_status")
	}
	return nil
}
