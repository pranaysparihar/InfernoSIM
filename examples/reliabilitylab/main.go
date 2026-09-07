// reliabilitylab is a synthetic refund/rollback control loop, not a production
// payment integration. All hosts are served by the local InfernoSIM proxy.
package main

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"infernosim/pkg/event"
)

//go:embed replay.yaml
var config []byte

func main() {
	initDir := flag.String("init", "", "Create a new synthetic incident directory; never overwrite")
	mode := flag.String("mode", "safe", "safe or unsafe control loop")
	flag.Parse()
	var err error
	if *initDir != "" {
		err = initialize(*initDir)
	} else {
		err = run(*mode)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func request(tool, id, tenant string, version int) []byte {
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": "tools/call", "params": map[string]any{"name": tool, "arguments": map[string]any{"payment_id": "synthetic-payment", "tenant": tenant, "amount": 10, "version": version}}})
	return b
}

func initialize(dir string) error {
	if err := os.Mkdir(dir, 0o700); err != nil {
		return err
	}
	var log bytes.Buffer
	encoder := json.NewEncoder(&log)
	fixtures := []struct{ tool, body string }{
		{"approve", `{"result":{"approved":true}}`},
		{"payment.refund", `{"result":{"status":"refunded"}}`},
		{"payment.refund_status", `{"result":{"status":"refunded"}}`},
		{"monitor", `{"result":{"healthy":false,"version":1}}`},
		{"payment.undo", `{"result":{"status":"reversed"}}`},
	}
	for i, f := range fixtures {
		req := request(f.tool, "fixture", "tenant-a", 1)
		e := event.Event{ID: f.tool, Type: "OutboundCall", Sequence: int64(i + 1), Method: "POST", URL: "http://mcp.test/mcp", Status: 200, BodyB64: base64.StdEncoding.EncodeToString(req), ResponseCaptured: true, ResponseHeaders: http.Header{"Content-Type": {"application/json"}}, ResponseBodyB64: base64.StdEncoding.EncodeToString([]byte(f.body))}
		if err := encoder.Encode(e); err != nil {
			return err
		}
	}
	if err := encoder.Encode(event.Event{ID: "provider", Type: "OutboundCall", Method: "POST", URL: "http://llm.test/v1/chat/completions", Status: 200, BodyB64: base64.StdEncoding.EncodeToString([]byte(`{}`)), ResponseCaptured: true, ResponseHeaders: http.Header{"Content-Type": {"application/json"}}, ResponseBodyB64: base64.StdEncoding.EncodeToString([]byte(`{"choices":[],"usage":{"total_tokens":4}}`))}); err != nil {
		return err
	}
	for name, data := range map[string][]byte{"inbound.log": {}, "outbound.log": log.Bytes(), "replay.yaml": config} {
		file, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		_, writeErr := file.Write(data)
		closeErr := file.Close()
		if writeErr != nil {
			return writeErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	fmt.Printf("Created synthetic reliability incident: %s\n", dir)
	return nil
}

func run(mode string) error {
	if mode != "safe" && mode != "unsafe" {
		return fmt.Errorf("mode must be safe or unsafe")
	}
	proxy, err := url.Parse(os.Getenv("INFERNOSIM_PROXY_URL"))
	if err != nil || proxy.Host == "" {
		return fmt.Errorf("run under infernosim agent run/compare")
	}
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxy), DisableKeepAlives: true}, Timeout: 5 * time.Second}
	defer client.CloseIdleConnections()
	call := func(endpoint string, body []byte) (map[string]any, error) {
		req, err := http.NewRequestWithContext(context.Background(), "POST", endpoint, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			return nil, fmt.Errorf("synthetic dependency status %d", resp.StatusCode)
		}
		var result map[string]any
		err = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&result)
		return result, err
	}
	tool := func(name, id, tenant string, version int) (map[string]any, error) {
		return call("http://mcp.test/mcp", request(name, id, tenant, version))
	}
	recovered := os.Getenv("INFERNOSIM_ATTEMPT") == "1"
	checkpoint := filepath.Join(os.Getenv("INFERNOSIM_CHECKPOINT_DIR"), "refund-intent")
	if recovered {
		if data, err := os.ReadFile(checkpoint); err != nil || string(data) != "synthetic-payment" {
			return fmt.Errorf("missing durable refund intent")
		}
		if mode == "safe" {
			if _, err := tool("payment.refund_status", "verify", "tenant-a", 1); err != nil {
				return err
			}
		}
	} else if mode == "safe" {
		approval, err := tool("approve", "approve", "tenant-a", 0)
		if err != nil {
			return err
		}
		result, _ := approval["result"].(map[string]any)
		if result["approved"] != true {
			return fmt.Errorf("approval unavailable")
		}
	}
	if !recovered || mode == "unsafe" {
		if os.Getenv("INFERNOSIM_CHECKPOINT_DIR") == "" {
			return fmt.Errorf("missing checkpoint directory")
		}
		if err := os.WriteFile(checkpoint, []byte("synthetic-payment"), 0o600); err != nil {
			return err
		}
		tenant := "tenant-a"
		if mode == "unsafe" {
			tenant = "tenant-b"
		}
		if _, err := tool("payment.refund", "refund", tenant, 0); err != nil {
			return err
		}
		if mode == "unsafe" {
			if _, err := tool("payment.refund", "retry", tenant, 0); err != nil {
				return err
			}
		}
	}
	version := 1
	if mode == "unsafe" {
		version = 0
	}
	if _, err := tool("monitor", "monitor", "tenant-a", version); err != nil {
		return err
	}
	if mode == "safe" {
		if _, err := tool("payment.undo", "undo", "tenant-a", 1); err != nil {
			return err
		}
	}
	count := 1
	if mode == "unsafe" {
		count = 3
	}
	for i := 0; i < count; i++ {
		if _, err := call("http://llm.test/v1/chat/completions", []byte(`{}`)); err != nil {
			return err
		}
	}
	fmt.Printf("Synthetic %s control loop finished; inspect independent simulator assertions.\n", mode)
	return nil
}
