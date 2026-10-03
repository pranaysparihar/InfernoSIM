// Package mcpproxy records and deterministically replays newline-delimited MCP
// stdio traffic. It never writes diagnostics to stdout because stdout is the
// MCP protocol channel.
package mcpproxy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"infernosim/pkg/agentreliability"
)

const (
	TranscriptVersion  = 1
	MaximumMessageSize = 16 * 1024 * 1024
	MaximumRecords     = 100_000
)

var ErrResponseLost = errors.New("injected MCP stdio response loss")

type Record struct {
	Version   int             `json:"version"`
	Sequence  int64           `json:"sequence"`
	Direction string          `json:"direction"`
	Timestamp time.Time       `json:"timestamp"`
	Message   json.RawMessage `json:"message"`
}

type recorder struct {
	mu       sync.Mutex
	encoder  *json.Encoder
	sequence int64
}

func (r *recorder) write(direction string, message []byte) error {
	if len(message) == 0 || len(message) > MaximumMessageSize || !json.Valid(message) {
		return fmt.Errorf("MCP stdio message is invalid or exceeds %d bytes", MaximumMessageSize)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sequence++
	return r.encoder.Encode(Record{
		Version: TranscriptVersion, Sequence: r.sequence, Direction: direction,
		Timestamp: time.Now().UTC(), Message: append(json.RawMessage(nil), message...),
	})
}

type RecordOptions struct {
	OutputPath string
	Command    []string
	Stdin      io.Reader
	Stdout     io.Writer
	Stderr     io.Writer
	Force      bool
}

func RecordCommand(ctx context.Context, opts RecordOptions) error {
	if len(opts.Command) == 0 {
		return fmt.Errorf("an explicit MCP server command is required")
	}
	if strings.TrimSpace(opts.OutputPath) == "" {
		return fmt.Errorf("MCP transcript output path is required")
	}
	if opts.Stdin == nil {
		opts.Stdin = os.Stdin
	}
	if opts.Stdout == nil {
		opts.Stdout = os.Stdout
	}
	if opts.Stderr == nil {
		opts.Stderr = os.Stderr
	}
	directory := filepath.Dir(opts.OutputPath)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	if info, statErr := os.Lstat(opts.OutputPath); statErr == nil {
		if info.IsDir() || !opts.Force {
			return fmt.Errorf("refusing to overwrite existing MCP transcript %q", opts.OutputPath)
		}
	} else if !os.IsNotExist(statErr) {
		return statErr
	}
	file, err := os.CreateTemp(directory, ".infernosim-mcp-*")
	if err != nil {
		return err
	}
	tempPath := file.Name()
	defer os.Remove(tempPath)
	defer file.Close()
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return err
	}
	log := &recorder{encoder: json.NewEncoder(file)}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	command := exec.CommandContext(ctx, opts.Command[0], opts.Command[1:]...)
	childInput, err := command.StdinPipe()
	if err != nil {
		return err
	}
	defer childInput.Close()
	childOutput, err := command.StdoutPipe()
	if err != nil {
		return err
	}
	defer childOutput.Close()
	command.Stderr = opts.Stderr
	if err := command.Start(); err != nil {
		_ = file.Close()
		return err
	}
	serverErr := make(chan error, 1)
	go func() {
		err := copyLines(childOutput, opts.Stdout, func(message []byte) error {
			return log.write("server_to_client", message)
		})
		if err != nil {
			cancel()
		}
		serverErr <- err
	}()
	clientErr := copyLines(opts.Stdin, childInput, func(message []byte) error {
		return log.write("client_to_server", message)
	})
	_ = childInput.Close()
	if clientErr != nil {
		cancel()
	}
	// Wait closes StdoutPipe: drain it before reaping an exited server.
	outputErr := <-serverErr
	waitErr := command.Wait()
	if err := errors.Join(clientErr, outputErr, waitErr, file.Sync(), file.Close()); err != nil {
		return err
	}
	if runtime.GOOS == "windows" && opts.Force {
		if err := os.Remove(opts.OutputPath); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return os.Rename(tempPath, opts.OutputPath)
}

func copyLines(input io.Reader, output io.Writer, observe func([]byte) error) error {
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 64*1024), MaximumMessageSize)
	for scanner.Scan() {
		message := append([]byte(nil), scanner.Bytes()...)
		if err := observe(message); err != nil {
			return err
		}
		if _, err := output.Write(append(message, '\n')); err != nil {
			return err
		}
	}
	return scanner.Err()
}

type ReplayOptions struct {
	Context        context.Context
	TranscriptPath string
	Stdin          io.Reader
	Stdout         io.Writer
	Engine         *agentreliability.Engine
}

func Replay(opts ReplayOptions) error {
	if opts.Context == nil {
		opts.Context = context.Background()
	}
	if opts.Stdin == nil {
		opts.Stdin = os.Stdin
	}
	if opts.Stdout == nil {
		opts.Stdout = os.Stdout
	}
	records, err := Load(opts.TranscriptPath)
	if err != nil {
		return err
	}
	scanner := bufio.NewScanner(opts.Stdin)
	scanner.Buffer(make([]byte, 64*1024), MaximumMessageSize)
	position := 0
	pending := map[string][]byte{}
	runtimeIDs := map[string]json.RawMessage{}
	activeIDs := map[string]bool{}
	cancelledPending := map[string]bool{}
clientLoop:
	for scanner.Scan() {
		if err := opts.Context.Err(); err != nil {
			return err
		}
		request := append([]byte(nil), scanner.Bytes()...)
		if !json.Valid(request) {
			return fmt.Errorf("MCP client sent invalid JSON")
		}
		for position < len(records) && records[position].Direction != "client_to_server" {
			position++
		}
		if position >= len(records) {
			return fmt.Errorf("MCP replay divergence: unexpected client message")
		}
		expectedRequest := remapCancellation(records[position].Message, runtimeIDs)
		if !equivalentRequest(expectedRequest, request) {
			return fmt.Errorf("MCP replay divergence at transcript sequence %d", records[position].Sequence)
		}
		currentID, hasID := messageID(request)
		recordedID, recordedHasID := messageID(records[position].Message)
		if hasID != recordedHasID {
			return fmt.Errorf("MCP replay divergence: request/notification shape changed")
		}
		if hasID {
			if activeIDs[string(currentID)] || pending[string(recordedID)] != nil {
				return fmt.Errorf("MCP replay divergence: duplicate in-flight request ID")
			}
			pending[string(recordedID)] = request
			runtimeIDs[string(recordedID)] = currentID
			activeIDs[string(currentID)] = true
		} else if opts.Engine != nil {
			var notification struct {
				Method string `json:"method"`
				Params struct {
					RequestID json.RawMessage `json:"requestId"`
				} `json:"params"`
			}
			if json.Unmarshal(request, &notification) == nil && notification.Method == "notifications/cancelled" {
				for recorded, id := range runtimeIDs {
					if bytes.Equal(id, notification.Params.RequestID) && pending[recorded] != nil {
						cancelledPending[recorded] = true
					}
				}
			}
			_, err := opts.Engine.ProcessContext(opts.Context, agentreliability.Request{Method: "STDIO", Host: "stdio.mcp", Path: "/stdio", Body: request}, agentreliability.Response{Status: http.StatusNoContent})
			if err != nil {
				return err
			}
		}
		position++
		for position < len(records) && records[position].Direction == "server_to_client" {
			response := append([]byte(nil), records[position].Message...)
			responseID, responseHasID := messageID(response)
			matchedRequest := pending[string(responseID)]
			if responseHasID {
				if matchedRequest == nil {
					return fmt.Errorf("MCP transcript response has no pending client request; server-initiated requests are unsupported")
				}
				id := runtimeIDs[string(responseID)]
				response = replaceMessageID(response, id)
				delete(pending, string(responseID))
				delete(activeIDs, string(id))
			}
			if opts.Engine != nil && responseHasID {
				decision, processErr := opts.Engine.ProcessContext(opts.Context, agentreliability.Request{
					Method: "STDIO", Host: "stdio.mcp", Path: "/stdio", Headers: make(http.Header), Body: matchedRequest,
					InFlightAtCancel: cancelledPending[string(responseID)],
				}, agentreliability.Response{Status: http.StatusOK, Headers: make(http.Header), Body: response})
				if processErr != nil {
					return processErr
				}
				delete(cancelledPending, string(responseID))
				if decision.Delay > 0 {
					if err := wait(opts.Context, decision.Delay); err != nil {
						return err
					}
				}
				if decision.Timeout > 0 {
					if err := wait(opts.Context, decision.Timeout); err != nil {
						return err
					}
					position++
					continue clientLoop
				}
				if decision.Reset {
					return ErrResponseLost
				}
				if decision.ResponseLost {
					position++
					continue clientLoop
				}
				response = decision.Response.Body
			}
			if _, err := opts.Stdout.Write(append(response, '\n')); err != nil {
				return err
			}
			position++
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if position != len(records) || len(pending) > 0 {
		return fmt.Errorf("MCP replay incomplete: transcript or pending requests remain")
	}
	return nil
}

func remapCancellation(message []byte, ids map[string]json.RawMessage) []byte {
	var object map[string]json.RawMessage
	if json.Unmarshal(message, &object) != nil || string(object["method"]) != `"notifications/cancelled"` {
		return message
	}
	var params map[string]json.RawMessage
	if json.Unmarshal(object["params"], &params) != nil {
		return message
	}
	if id, ok := ids[string(params["requestId"])]; ok {
		params["requestId"] = id
		object["params"], _ = json.Marshal(params)
		data, _ := json.Marshal(object)
		return data
	}
	return message
}

func wait(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func Load(path string) ([]Record, error) {
	file, err := os.Open(filepath.Clean(path))
	if err != nil {
		return nil, err
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, int64(MaximumRecords)*MaximumMessageSize))
	var records []Record
	for decoder.More() {
		var record Record
		if err := decoder.Decode(&record); err != nil {
			return nil, err
		}
		if len(records) >= MaximumRecords {
			return nil, fmt.Errorf("MCP transcript exceeds %d records", MaximumRecords)
		}
		if record.Version != TranscriptVersion || (record.Direction != "client_to_server" && record.Direction != "server_to_client") || !json.Valid(record.Message) {
			return nil, fmt.Errorf("invalid MCP transcript record at index %d", len(records))
		}
		records = append(records, record)
	}
	if len(records) == 0 {
		return nil, fmt.Errorf("MCP transcript is empty")
	}
	return records, nil
}

func equivalentRequest(expected, actual []byte) bool {
	var left, right any
	if json.Unmarshal(expected, &left) != nil || json.Unmarshal(actual, &right) != nil {
		return false
	}
	removeID(left)
	removeID(right)
	leftJSON, _ := json.Marshal(left)
	rightJSON, _ := json.Marshal(right)
	return bytes.Equal(leftJSON, rightJSON)
}

func removeID(value any) {
	if object, ok := value.(map[string]any); ok {
		delete(object, "id")
	}
}

func messageID(message []byte) (json.RawMessage, bool) {
	var object map[string]json.RawMessage
	if json.Unmarshal(message, &object) != nil {
		return nil, false
	}
	id, exists := object["id"]
	return append(json.RawMessage(nil), id...), exists
}

func replaceMessageID(message []byte, id json.RawMessage) []byte {
	var object map[string]json.RawMessage
	if json.Unmarshal(message, &object) != nil {
		return message
	}
	if _, exists := object["id"]; !exists {
		return message
	}
	object["id"] = append(json.RawMessage(nil), id...)
	encoded, err := json.Marshal(object)
	if err != nil {
		return message
	}
	return encoded
}
