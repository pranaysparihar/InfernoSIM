// Command ollamasmoke checks that a local Ollama model emits the tool-call
// envelope understood by InfernoSIM. It is a compatibility check, not a
// deterministic release gate: the recorded fixture remains the CI oracle.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"infernosim/pkg/agentreliability"
)

func main() {
	endpoint := flag.String("endpoint", "http://127.0.0.1:11434", "Ollama base URL")
	model := flag.String("model", "llama3.1-local:latest", "Local model with tool support")
	timeout := flag.Duration("timeout", 2*time.Minute, "Request timeout")
	flag.Parse()
	if err := run(*endpoint, *model, *timeout, http.DefaultClient); err != nil {
		fmt.Fprintln(os.Stderr, "OLLAMA_SMOKE: FAIL:", err)
		os.Exit(1)
	}
	fmt.Printf("OLLAMA_SMOKE: PASS (model=%s)\n", *model)
}

func run(endpoint, model string, timeout time.Duration, client *http.Client) error {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return fmt.Errorf("endpoint must be an absolute HTTP(S) URL")
	}
	if strings.TrimSpace(model) == "" || timeout <= 0 || timeout > 10*time.Minute {
		return fmt.Errorf("model is required and timeout must be between 1ns and 10m")
	}
	payload := map[string]any{
		"model":  model,
		"stream": false,
		"messages": []map[string]string{{
			"role": "user", "content": "Check whether payment pay_1 can be refunded. Use the policy_check tool.",
		}},
		"tools": []map[string]any{{
			"type": "function",
			"function": map[string]any{
				"name": "policy_check", "description": "Check refund policy",
				"parameters": map[string]any{
					"type": "object", "required": []string{"payment_id"},
					"properties": map[string]any{"payment_id": map[string]string{"type": "string"}},
				},
			},
		}},
	}
	encoded, _ := json.Marshal(payload)
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(endpoint, "/")+"/api/chat", bytes.NewReader(encoded))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 4*1024*1024+1))
	if err != nil {
		return err
	}
	if len(body) > 4*1024*1024 {
		return fmt.Errorf("response exceeds 4 MiB")
	}
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("Ollama returned %s", response.Status)
	}
	config := agentreliability.Config{
		Enabled: true,
		Adapters: agentreliability.Adapters{Providers: []agentreliability.ProviderAdapter{{
			Name: "ollama-local", Type: "ollama", HostRegex: "^" + strings.ReplaceAll(parsed.Hostname(), ".", `\.`) + "$",
		}}},
	}
	engine, err := agentreliability.NewEngine(config, nil)
	if err != nil {
		return err
	}
	_, err = engine.Process(agentreliability.Request{
		Method: http.MethodPost, Host: parsed.Hostname(), Path: "/api/chat", Headers: request.Header.Clone(), Body: encoded,
	}, agentreliability.Response{Status: response.StatusCode, Headers: response.Header.Clone(), Body: body})
	if err != nil {
		return err
	}
	snapshot := engine.Snapshot()
	if len(snapshot.Calls) != 1 || snapshot.Calls[0].Provider != "ollama-local" || snapshot.Calls[0].Tool != "policy_check" {
		return fmt.Errorf("model did not emit the expected policy_check tool-call envelope")
	}
	return nil
}
