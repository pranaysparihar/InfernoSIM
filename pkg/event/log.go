package event

import (
	"bufio"
	"encoding/json"
	"os"
	"sync"
)

type Logger struct {
	mu  sync.Mutex
	f   *os.File
	w   *bufio.Writer
	enc *json.Encoder
}

func NewLogger(path string) (*Logger, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}

	w := bufio.NewWriterSize(f, 1<<20) // 1MB buffer
	enc := json.NewEncoder(w)
	// IMPORTANT: json.Encoder.Encode() always appends '\n'
	enc.SetEscapeHTML(false)

	return &Logger{
		f:   f,
		w:   w,
		enc: enc,
	}, nil
}

func (l *Logger) Write(e *Event) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if err := l.enc.Encode(e); err != nil {
		return err
	}
	// Flush so the file is always valid JSONL even if the process crashes later.
	return l.w.Flush()
}

func (l *Logger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()

	_ = l.w.Flush()
	return l.f.Close()
}
