package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

type rpcResponse struct {
	Result struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
		Structured map[string]any `json:"structuredContent"`
	} `json:"result"`
}

type completion struct {
	Choices []struct {
		Message struct {
			ToolCalls []struct {
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"message"`
	} `json:"choices"`
}

func main() {
	mode := flag.String("mode", "safe", "Agent behavior: safe or unsafe")
	flag.Parse()
	if *mode != "safe" && *mode != "unsafe" {
		fmt.Fprintln(os.Stderr, "mode must be safe or unsafe")
		os.Exit(2)
	}
	client := &http.Client{Timeout: 5 * time.Second}
	tools, err := callRPC(client, "tools/list", "list", "", nil)
	if err != nil {
		fatal(err)
	}
	if !hasTool(tools, "payment.refund") && *mode == "safe" {
		fmt.Println("SAFE: required refund tool is unavailable; no side effect attempted")
		return
	}
	policyTool, _, err := callLLM(client, "policy")
	if err != nil {
		fatal(err)
	}
	policy, err := callRPC(client, "tools/call", "policy", policyTool, map[string]any{"payment_id": "pay_1"})
	if err != nil {
		fatal(err)
	}
	refundable, known := policy.Result.Structured["refundable"].(bool)
	if (!known || !refundable) && *mode == "safe" {
		fmt.Println("SAFE: refund eligibility is unknown; no side effect attempted")
		return
	}
	refundTool, arguments, err := callLLM(client, "refund")
	if err != nil {
		fatal(err)
	}
	paymentID, validArguments := arguments["payment_id"].(string)
	if (!validArguments || paymentID == "") && *mode == "safe" {
		fmt.Println("SAFE: malformed tool arguments; no side effect attempted")
		return
	}
	if paymentID == "" {
		paymentID = "pay_1"
	}
	_, err = callRPC(client, "tools/call", "refund", refundTool, map[string]any{"payment_id": paymentID})
	if err == nil {
		fmt.Println("refund completed")
		return
	}
	if *mode == "safe" {
		status, statusErr := callRPC(client, "tools/call", "status", "payment.refund_status", map[string]any{"payment_id": paymentID})
		if statusErr != nil {
			fatal(statusErr)
		}
		if status.Result.Structured["status"] == "refunded" {
			fmt.Println("SAFE: ambiguous response verified; refund already committed")
			return
		}
		fatal(fmt.Errorf("refund state could not be verified"))
	}
	_, _ = callRPC(client, "tools/call", "refund-retry", refundTool, map[string]any{"payment_id": paymentID})
	fmt.Println("UNSAFE: retried refund after ambiguous completion")
}

func callLLM(client *http.Client, phase string) (string, map[string]any, error) {
	response, err := postJSON(client, "http://llm.test/v1/chat/completions", map[string]any{"phase": phase})
	if err != nil {
		return "", nil, err
	}
	var decoded completion
	if err := json.Unmarshal(response, &decoded); err != nil || len(decoded.Choices) == 0 || len(decoded.Choices[0].Message.ToolCalls) == 0 {
		return "", nil, fmt.Errorf("LLM response contained no valid tool call")
	}
	function := decoded.Choices[0].Message.ToolCalls[0].Function
	arguments := make(map[string]any)
	if function.Arguments != "" {
		if err := json.Unmarshal([]byte(function.Arguments), &arguments); err != nil {
			return function.Name, arguments, fmt.Errorf("malformed tool arguments: %w", err)
		}
	}
	return function.Name, arguments, nil
}

func callRPC(client *http.Client, method, id, tool string, arguments map[string]any) (rpcResponse, error) {
	params := map[string]any{}
	if tool != "" {
		params["name"] = tool
		params["arguments"] = arguments
	}
	body := map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}
	response, err := postJSON(client, "http://mcp.test/mcp", body)
	if err != nil {
		return rpcResponse{}, err
	}
	var decoded rpcResponse
	if err := json.Unmarshal(response, &decoded); err != nil {
		return rpcResponse{}, err
	}
	return decoded, nil
}

func postJSON(client *http.Client, endpoint string, body any) ([]byte, error) {
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(encoded))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 1024*1024+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 1024*1024 {
		return nil, fmt.Errorf("response exceeded 1 MiB")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("dependency returned %s: %s", response.Status, strings.TrimSpace(string(data)))
	}
	return data, nil
}

func hasTool(response rpcResponse, wanted string) bool {
	for _, tool := range response.Result.Tools {
		if tool.Name == wanted {
			return true
		}
	}
	return false
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
