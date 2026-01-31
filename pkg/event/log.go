package event

import (
    "bufio"
    "encoding/json"
    "os"
    "sync"
)

// Logger writes events to a file (thread-safe).
type Logger struct {
    file   *os.File
    writer *bufio.Writer
    mu     sync.Mutex
}

// NewLogger opens the given file (creating it if needed) and returns a Logger.
func NewLogger(filePath string) (*Logger, error) {
    f, err := os.Create(filePath)
    if err != nil {
        return nil, err
    }
    return &Logger{
        file:   f,
        writer: bufio.NewWriter(f),
    }, nil
}

// Write writes an Event to the log as a JSON line.
func (l *Logger) Write(evt *Event) {
    // Marshal the event to JSON
    data, err := json.Marshal(evt)
    if err != nil {
        // If marshal fails, we still attempt to proceed (or could log to stderr)
        return
    }
    l.mu.Lock()
    defer l.mu.Unlock()
    l.writer.Write(data)
    l.writer.WriteByte('\n')
    l.writer.Flush()
}

// Close flushes and closes the underlying file.
func (l *Logger) Close() error {
    l.mu.Lock()
    defer l.mu.Unlock()
    // Flush any buffered data
    if err := l.writer.Flush(); err != nil {
        return err
    }
    return l.file.Close()
}