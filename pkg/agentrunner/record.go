package agentrunner

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"infernosim/pkg/capture"
	"infernosim/pkg/event"
	"infernosim/pkg/mcpproxy"
	"infernosim/pkg/privacy"
	"infernosim/pkg/traceorder"
)

type RecordOptions struct {
	Incident                                       string
	Command, MCPCommand, MCPConnector              []string
	Environment                                    []string
	Timeout                                        time.Duration
	CaptureSensitiveData, AllowPrivateDestinations bool
	Privacy                                        *privacy.Policy
}

func Record(ctx context.Context, o RecordOptions) (ProcessResult, error) {
	var result ProcessResult
	if o.Incident == "" || len(o.Command) == 0 {
		return result, fmt.Errorf("record requires new incident directory and explicit application command")
	}
	if o.Timeout == 0 {
		o.Timeout = DefaultTimeout
	}
	if o.Timeout < time.Millisecond || o.Timeout > MaximumTimeout {
		return result, fmt.Errorf("invalid record timeout")
	}
	if len(o.MCPCommand) > 0 && !o.CaptureSensitiveData && (o.Privacy == nil || !o.Privacy.CaptureBodies) {
		return result, fmt.Errorf("MCP recording requires explicit --capture-sensitive-data or a privacy policy with capture_bodies")
	}
	if err := os.Mkdir(o.Incident, 0700); err != nil {
		return result, err
	}
	for name, data := range map[string]string{"incident.json": "{\"schema_version\":1}", "inbound.log": "", "replay.yaml": "agent:\n  enabled: true\n  adapters:\n    mcp: true\n"} {
		if err := os.WriteFile(filepath.Join(o.Incident, name), []byte(data), 0600); err != nil {
			return result, err
		}
	}
	logger, err := event.NewLogger(filepath.Join(o.Incident, "outbound.log"))
	if err != nil {
		return result, err
	}
	defer logger.Close()
	clock := traceorder.New()
	proxy, err := capture.StartForwardProxy("127.0.0.1:0", &capture.ProxyContext{Logger: logger, Order: clock, CaptureSensitiveData: o.CaptureSensitiveData, AllowPrivateDestinations: o.AllowPrivateDestinations, Privacy: o.Privacy})
	if err != nil {
		return result, err
	}
	ctx, cancel := context.WithTimeout(ctx, o.Timeout)
	defer cancel()
	defer proxy.Close()
	address := "http://" + proxy.Addr
	env := mergeEnvironment(os.Environ(), o.Environment, map[string]string{"HTTP_PROXY": address, "http_proxy": address, "HTTPS_PROXY": address, "https_proxy": address, "NO_PROXY": "", "no_proxy": "", "INFERNOSIM_PROXY_URL": address})
	var bridge *mcpproxy.Bridge
	if len(o.MCPCommand) > 0 {
		opts := mcpproxy.RecordOptions{OutputPath: filepath.Join(o.Incident, "mcp.log"), Command: o.MCPCommand, Order: clock, Stderr: io.Discard}
		if o.Privacy != nil {
			opts.Transform = o.Privacy.ApplyBody
		}
		bridge, err = mcpproxy.StartRecordBridge(ctx, opts)
		if err != nil {
			return result, err
		}
		defer bridge.Close()
		connector := o.MCPConnector
		if len(connector) == 0 {
			connector = []string{executablePath(), "agent", "mcp", "connect"}
		}
		argv, _ := json.Marshal(connector)
		env = mergeEnvironment(env, nil, map[string]string{"INFERNOSIM_MCP_COMMAND_JSON": string(argv), "INFERNOSIM_MCP_ENDPOINT": bridge.Address, "INFERNOSIM_MCP_TOKEN": bridge.Token})
	}
	result, err = executeWithRestart(ctx, Options{Command: o.Command, OutputLimit: DefaultOutputLimit}, env, nil, make(chan struct{}))
	if bridge != nil {
		if e := bridge.Close(); e != nil {
			return result, e
		}
	}
	shutdown, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	if e := proxy.Shutdown(shutdown); e != nil {
		return result, e
	}
	if err != nil {
		return result, fmt.Errorf("recorded application failed; partial incident retained: %w", err)
	}
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	if err = logger.Close(); err != nil {
		return result, err
	}
	if err = WriteJSON(filepath.Join(o.Incident, "capture-session.json"), clock.Snapshot()); err != nil {
		return result, err
	}
	return result, nil
}
