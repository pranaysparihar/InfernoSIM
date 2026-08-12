package message

import (
	"bufio"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"infernosim/pkg/event"
)

const FormatVersion = 1

type Header struct {
	Key      string `json:"key"`
	ValueB64 string `json:"value_b64"`
}

type BinaryHeader struct {
	Key   string
	Value []byte
}

type Record struct {
	Version       int       `json:"version"`
	ID            string    `json:"id"`
	Timestamp     time.Time `json:"timestamp"`
	Sequence      int64     `json:"sequence"`
	Broker        string    `json:"broker,omitempty"`
	Direction     string    `json:"direction,omitempty"`
	Topic         string    `json:"topic"`
	Partition     int32     `json:"partition"`
	Offset        int64     `json:"offset"`
	KeyB64        string    `json:"key_b64,omitempty"`
	Headers       []Header  `json:"headers,omitempty"`
	PayloadB64    string    `json:"payload_b64"`
	PayloadSHA256 string    `json:"payload_sha256"`
	Redacted      bool      `json:"redacted,omitempty"`
	CorrelationID string    `json:"correlation_id,omitempty"`
	Schema        string    `json:"schema,omitempty"`
}

func New(topic string, key, payload []byte, headers map[string][]byte) Record {
	headerNames := make([]string, 0, len(headers))
	for name := range headers {
		headerNames = append(headerNames, name)
	}
	sort.Strings(headerNames)
	binaryHeaders := make([]BinaryHeader, 0, len(headerNames))
	for _, name := range headerNames {
		binaryHeaders = append(binaryHeaders, BinaryHeader{Key: name, Value: headers[name]})
	}
	return NewWithHeaders(topic, key, payload, binaryHeaders)
}

func NewWithHeaders(topic string, key, payload []byte, headers []BinaryHeader) Record {
	encodedHeaders := make([]Header, 0, len(headers))
	for _, header := range headers {
		encodedHeaders = append(encodedHeaders, Header{Key: header.Key, ValueB64: base64.StdEncoding.EncodeToString(header.Value)})
	}
	return Record{
		Version:       FormatVersion,
		ID:            event.GenerateID(),
		Timestamp:     time.Now().UTC(),
		Topic:         topic,
		Partition:     -1,
		Offset:        -1,
		KeyB64:        base64.StdEncoding.EncodeToString(key),
		Headers:       encodedHeaders,
		PayloadB64:    base64.StdEncoding.EncodeToString(payload),
		PayloadSHA256: SHA256(payload),
		CorrelationID: inferCorrelation(headers, payload),
	}
}

func (r Record) Key() ([]byte, error) {
	return decode("key", r.KeyB64)
}

func (r Record) Payload() ([]byte, error) {
	payload, err := decode("payload", r.PayloadB64)
	if err != nil {
		return nil, err
	}
	if r.PayloadSHA256 != "" && SHA256(payload) != r.PayloadSHA256 {
		return nil, fmt.Errorf("message %s payload hash mismatch", r.ID)
	}
	return payload, nil
}

func (r Record) HeaderMap() (map[string][]byte, error) {
	headers, err := r.HeaderValues()
	if err != nil {
		return nil, err
	}
	result := make(map[string][]byte, len(headers))
	for _, header := range headers {
		result[header.Key] = header.Value
	}
	return result, nil
}

func (r Record) HeaderValues() ([]BinaryHeader, error) {
	result := make([]BinaryHeader, 0, len(r.Headers))
	for _, header := range r.Headers {
		if strings.TrimSpace(header.Key) == "" {
			return nil, fmt.Errorf("message %s has an empty header name", r.ID)
		}
		value, err := decode("header "+header.Key, header.ValueB64)
		if err != nil {
			return nil, err
		}
		result = append(result, BinaryHeader{Key: header.Key, Value: value})
	}
	return result, nil
}

func Load(path string) ([]Record, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	decoder := json.NewDecoder(bufio.NewReader(file))
	var records []Record
	seenIDs := make(map[string]bool)
	for {
		var record Record
		if err := decoder.Decode(&record); err != nil {
			if err == io.EOF {
				break
			}
			return nil, fmt.Errorf("decode message log: %w", err)
		}
		if err := record.Validate(); err != nil {
			return nil, err
		}
		if seenIDs[record.ID] {
			return nil, fmt.Errorf("message log contains duplicate id %q", record.ID)
		}
		seenIDs[record.ID] = true
		records = append(records, record)
	}
	sort.SliceStable(records, func(i, j int) bool {
		if records[i].Timestamp.Equal(records[j].Timestamp) {
			return records[i].Sequence < records[j].Sequence
		}
		return records[i].Timestamp.Before(records[j].Timestamp)
	})
	return records, nil
}

func (r Record) Validate() error {
	if r.Version != FormatVersion {
		return fmt.Errorf("message %s uses unsupported format version %d", r.ID, r.Version)
	}
	if r.ID == "" || r.Topic == "" || r.Timestamp.IsZero() {
		return fmt.Errorf("message requires id, timestamp, and topic")
	}
	if r.Direction != "" && r.Direction != "publish" && r.Direction != "consume" {
		return fmt.Errorf("message %s direction must be publish or consume", r.ID)
	}
	if _, err := r.Key(); err != nil {
		return err
	}
	if _, err := r.Payload(); err != nil {
		return err
	}
	if _, err := r.HeaderMap(); err != nil {
		return err
	}
	return nil
}

type Logger struct {
	file     *os.File
	writer   *bufio.Writer
	encoder  *json.Encoder
	sequence int64
}

func NewLogger(path string) (*Logger, error) {
	if err := os.MkdirAll(filepathDir(path), 0o700); err != nil {
		return nil, err
	}
	existing, err := LoadIfExists(path)
	if err != nil {
		return nil, err
	}
	var sequence int64
	for _, record := range existing {
		if record.Sequence > sequence {
			sequence = record.Sequence
		}
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return nil, err
	}
	writer := bufio.NewWriter(file)
	return &Logger{file: file, writer: writer, encoder: json.NewEncoder(writer), sequence: sequence}, nil
}

func (l *Logger) Write(record Record) error {
	l.sequence++
	record.Sequence = l.sequence
	if record.Version == 0 {
		record.Version = FormatVersion
	}
	if err := record.Validate(); err != nil {
		return err
	}
	if err := l.encoder.Encode(record); err != nil {
		return err
	}
	return l.writer.Flush()
}

func (l *Logger) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	_ = l.writer.Flush()
	return l.file.Close()
}

func LoadIfExists(path string) ([]Record, error) {
	records, err := Load(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	return records, err
}

func SHA256(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

func decode(name, value string) ([]byte, error) {
	decoded, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		return nil, fmt.Errorf("decode %s: %w", name, err)
	}
	return decoded, nil
}

func inferCorrelation(headers []BinaryHeader, payload []byte) string {
	for _, wanted := range []string{"correlation-id", "x-correlation-id", "traceparent", "x-request-id"} {
		for _, header := range headers {
			if strings.EqualFold(header.Key, wanted) && len(header.Value) > 0 {
				return safeCorrelation(string(header.Value))
			}
		}
	}
	var object map[string]any
	if json.Unmarshal(payload, &object) == nil {
		for _, name := range []string{"correlation_id", "trace_id", "request_id"} {
			if value, ok := object[name].(string); ok {
				return safeCorrelation(value)
			}
		}
	}
	return ""
}

func safeCorrelation(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 256 {
		return SHA256([]byte(value))
	}
	return value
}

func filepathDir(path string) string {
	index := strings.LastIndexAny(path, `/\`)
	if index < 0 {
		return "."
	}
	return path[:index]
}
