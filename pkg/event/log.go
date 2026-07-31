package event

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"sync"
)

type Logger struct {
	mu       sync.Mutex
	f        *os.File
	w        *bufio.Writer
	enc      *json.Encoder
	sequence int64 // atomic monotonic counter
}

func NewLogger(path string) (*Logger, error) {
	// Keep a seekable read/write handle while repairing a partial JSONL tail.
	// Windows does not permit truncating a handle opened with append semantics;
	// writes remain serialized by Logger.mu after the explicit seek to EOF below.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return nil, err
	}

	var lastSequence int64
	var validOffset int64
	dec := json.NewDecoder(f)
	for {
		var existing Event
		if err := dec.Decode(&existing); err != nil {
			if err != io.EOF {
				if truncateErr := f.Truncate(validOffset); truncateErr != nil {
					_ = f.Close()
					return nil, truncateErr
				}
			}
			break
		}
		validOffset = dec.InputOffset()
		if existing.Sequence > lastSequence {
			lastSequence = existing.Sequence
		}
	}
	if _, err := f.Seek(0, io.SeekEnd); err != nil {
		_ = f.Close()
		return nil, err
	}
	if info, err := f.Stat(); err != nil {
		_ = f.Close()
		return nil, err
	} else if info.Size() > 0 {
		last := []byte{0}
		if _, err := f.ReadAt(last, info.Size()-1); err != nil {
			_ = f.Close()
			return nil, err
		}
		if last[0] != '\n' {
			if _, err := f.Write([]byte{'\n'}); err != nil {
				_ = f.Close()
				return nil, err
			}
		}
	}

	w := bufio.NewWriterSize(f, 1<<20) // 1MB buffer
	enc := json.NewEncoder(w)
	// IMPORTANT: json.Encoder.Encode() always appends '\n'
	enc.SetEscapeHTML(false)

	return &Logger{
		f:        f,
		w:        w,
		enc:      enc,
		sequence: lastSequence,
	}, nil
}

func (l *Logger) Write(e *Event) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sequence++
	e.Sequence = l.sequence

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
