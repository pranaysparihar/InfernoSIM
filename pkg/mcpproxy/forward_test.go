package mcpproxy

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"testing"

	"infernosim/pkg/traceorder"
)

type recordingWriteFunc func([]byte) (int, error)

func (f recordingWriteFunc) Write(b []byte) (int, error) { return f(b) }

func TestForwardStartPrecedesPeerReactionAndRejectsShortWrites(t *testing.T) {
	var recorded bytes.Buffer
	clock := traceorder.New()
	r := recorder{encoder: json.NewEncoder(&recorded), order: clock}
	var reaction *traceorder.Stamp
	message := []byte(`{"jsonrpc":"2.0","id":1,"result":{}}`)
	err := r.forward("server_to_client", message, recordingWriteFunc(func(b []byte) (int, error) {
		reaction = clock.Next()
		return len(b), nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	var v Record
	if err := json.Unmarshal(recorded.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if v.ForwardStart == nil || v.ForwardStart.Sequence >= reaction.Sequence {
		t.Fatal("post-write callback changed observed causal order")
	}
	recorded.Reset()
	if err := r.forward("server_to_client", message, recordingWriteFunc(func(b []byte) (int, error) { return len(b) - 1, nil })); !errors.Is(err, io.ErrShortWrite) {
		t.Fatal("short write accepted", err)
	}
	if recorded.Len() != 0 {
		t.Fatal("incomplete write retained as a complete message")
	}
}
