package mcpproxy

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"

	"infernosim/pkg/agentreliability"
)

type Bridge struct {
	Address string
	Token   string
	cancel  context.CancelFunc
	done    chan struct{}
	mu      sync.Mutex
	err     error
}

type bufferedConnection struct {
	net.Conn
	reader *bufio.Reader
}

func (c *bufferedConnection) Read(p []byte) (int, error) { return c.reader.Read(p) }

// StartBridge keeps the MCP engine in the runner process, so HTTP and stdio
// share a single graph, fault ledger, restart hook, and proof. The child stdio
// connector forwards bytes only; it does not create a second scheduler.
func StartBridge(ctx context.Context, path string, engine *agentreliability.Engine) (*Bridge, error) {
	if engine == nil || engine.Causal() == nil {
		return nil, fmt.Errorf("MCP bridge requires causal scheduling")
	}
	if _, err := Load(path); err != nil {
		return nil, err
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	token := make([]byte, 32)
	if _, err = rand.Read(token); err != nil {
		_ = l.Close()
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	b := &Bridge{Address: l.Addr().String(), Token: hex.EncodeToString(token), cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(b.done)
		defer l.Close()
		stop := context.AfterFunc(ctx, func() { _ = l.Close() })
		defer stop()
		for attempts := 0; attempts < 16; attempts++ {
			conn, err := l.Accept()
			if err != nil {
				if ctx.Err() == nil {
					b.setError(err)
				}
				return
			}
			_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
			reader := bufio.NewReaderSize(conn, 256)
			line, err := reader.ReadSlice('\n')
			if err != nil || subtle.ConstantTimeCompare(line, []byte("InfernoSIM "+b.Token+"\n")) != 1 {
				_ = conn.Close()
				continue
			}
			_ = conn.SetReadDeadline(time.Time{})
			err = Replay(ReplayOptions{Context: ctx, TranscriptPath: path, Stdin: &bufferedConnection{Conn: conn, reader: reader}, Stdout: conn, Engine: engine, SharedController: true})
			_ = conn.Close()
			if err != nil {
				b.setError(err)
			}
			return
		}
		b.setError(fmt.Errorf("too many unauthenticated MCP bridge connections"))
	}()
	return b, nil
}
func (b *Bridge) setError(err error) { b.mu.Lock(); defer b.mu.Unlock(); b.err = err }
func (b *Bridge) Close() error {
	// Applications normally close their SDK transport before exiting. Give its
	// EOF and pending writes time to reach the runner before cancelling a read.
	select {
	case <-b.done:
	case <-time.After(100 * time.Millisecond):
		b.cancel()
		<-b.done
	}
	b.cancel()
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.err
}

// Connect is the explicit CLI stdio entry point used by real SDK subprocess
// transports. It only connects to a numeric loopback address supplied by Run.
func Connect(ctx context.Context, address, token string, input io.ReadCloser, output io.Writer) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		return fmt.Errorf("MCP connector requires a numeric loopback endpoint")
	}
	if len(token) != 64 || strings.ContainsAny(token, "\r\n") {
		return fmt.Errorf("MCP connector requires a runner token")
	}
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", address)
	if err != nil {
		return err
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close(); _ = input.Close() })
	defer stop()
	if _, err = fmt.Fprintf(conn, "InfernoSIM %s\n", token); err != nil {
		return err
	}
	inputDone := make(chan error, 1)
	go func() {
		_, err := io.Copy(conn, input)
		if tcp, ok := conn.(*net.TCPConn); ok {
			_ = tcp.CloseWrite()
		}
		inputDone <- err
	}()
	_, outputErr := io.Copy(output, conn)
	_ = input.Close()
	_ = conn.Close()
	inputErr := <-inputDone
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if outputErr != nil {
		return outputErr
	}
	return inputErr
}
