package agentreliability

import (
	"fmt"
	"net/http"

	"infernosim/pkg/toolstream"
)

func (e *Engine) providerType(name string) string {
	for _, p := range e.providers {
		if p.value.Name == name {
			return p.value.Type
		}
	}
	return ""
}

func (e *Engine) applyTargetedFault(decision *Decision, fault Fault, op Operation) error {
	metadata := fault
	metadata.Mutations = nil
	if err := applyFault(decision, metadata); err != nil {
		return err
	}
	for _, mutation := range fault.Mutations {
		if mutation.Target != "tool_arguments" {
			if err := applyFault(decision, Fault{Mutations: []Mutation{mutation}}); err != nil {
				return err
			}
			continue
		}
		if op.Kind != "llm_response" {
			return fmt.Errorf("tool_arguments requires a provider response")
		}
		var stream *toolstream.Stream
		var err error
		if toolstream.IsStream(decision.Response.Headers.Get("Content-Type")) {
			stream, err = toolstream.Parse(e.providerType(op.Provider), decision.Response.Body)
		} else {
			stream, err = toolstream.ParseEnvelope(e.providerType(op.Provider), decision.Response.Body)
		}
		if err != nil {
			return err
		}
		var matches []toolstream.Call
		for _, call := range stream.Calls {
			if (fault.Select.Tool == "" || fault.Select.Tool == call.Name) && (fault.Select.CallID == "" || fault.Select.CallID == call.ID) {
				matches = append(matches, call)
			}
		}
		if len(matches) != 1 {
			return fmt.Errorf("tool_arguments selector matched %d calls; use a unique call_id", len(matches))
		}
		mutation.Target = ""
		if err = stream.ReplaceArguments(matches[0].ID, func(arguments []byte) ([]byte, error) {
			d := Decision{Response: Response{Status: http.StatusOK, Headers: make(http.Header), Body: arguments}}
			err := applyFault(&d, Fault{Mutations: []Mutation{mutation}})
			return d.Response.Body, err
		}); err != nil {
			return err
		}
		decision.Response.Body, err = stream.Bytes()
		if err != nil {
			return err
		}
	}
	return nil
}
