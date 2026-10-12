// This acceptance helper exports real engine output for independent SDK consumers.
package main

import (
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	"infernosim/pkg/agentreliability"
)

func main() {
	out := flag.String("out", "", "New output directory")
	flag.Parse()
	if err := export(*out); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func export(out string) error {
	if out == "" {
		return fmt.Errorf("requires -out")
	}
	if err := os.Mkdir(out, 0700); err != nil {
		return err
	}
	for file, provider := range map[string]string{"responses.sse": "openai", "chat.sse": "openai", "anthropic.sse": "anthropic", "ollama.ndjson": "ollama"} {
		body, err := os.ReadFile(filepath.Join("pkg", "toolstream", "testdata", file))
		if err != nil {
			return err
		}
		id, contentType := "call_b", "text/event-stream"
		if provider == "ollama" {
			id, contentType = "ollama:1", "application/x-ndjson"
		}
		config := agentreliability.Config{Enabled: true, Adapters: agentreliability.Adapters{Providers: []agentreliability.ProviderAdapter{{Name: "model", Type: provider, HostRegex: "^fixture.test$"}}},
			Faults: []agentreliability.Fault{{ID: "amount", Select: agentreliability.Selector{Kind: "llm_response", Provider: "model", Tool: "lookup", CallID: id}, Mutations: []agentreliability.Mutation{{Target: "tool_arguments", Operation: "set", Path: "$.amount", Value: 99}}}}}
		for _, mode := range []string{"baseline", "mutated"} {
			var faults []string
			if mode == "mutated" {
				faults = []string{"amount"}
			}
			engine, err := agentreliability.NewEngine(config, faults)
			if err != nil {
				return err
			}
			decision, err := engine.Process(agentreliability.Request{Method: "POST", Host: "fixture.test", Body: []byte(`{}`)}, agentreliability.Response{Status: 200, Headers: http.Header{"Content-Type": {contentType}}, Body: body})
			if err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(out, mode+"-"+file), decision.Response.Body, 0600); err != nil {
				return err
			}
		}
	}
	return nil
}
