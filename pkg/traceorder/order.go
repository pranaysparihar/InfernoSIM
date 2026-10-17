// Package traceorder provides one process-local observation clock shared by
// HTTP and MCP capture. Wall clocks from unrelated recorders are never merged.
package traceorder

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
)

type Stamp struct {
	Session  string `json:"session"`
	Sequence uint64 `json:"sequence"`
}

type Clock struct {
	mu       sync.Mutex
	session  string
	sequence uint64
}

func (c *Clock) Snapshot() Stamp {
	c.mu.Lock()
	defer c.mu.Unlock()
	return Stamp{c.session, c.sequence}
}

func New() *Clock {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return &Clock{session: hex.EncodeToString(b)}
}

func (c *Clock) Next() *Stamp {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sequence++
	return &Stamp{Session: c.session, Sequence: c.sequence}
}
