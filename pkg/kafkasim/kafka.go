package kafkasim

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"regexp"
	"strings"
	"time"

	"infernosim/pkg/asyncapi"
	"infernosim/pkg/message"
	"infernosim/pkg/privacy"
	"infernosim/pkg/reporting"

	"github.com/twmb/franz-go/pkg/kgo"
	"github.com/twmb/franz-go/pkg/sasl/plain"
	"github.com/twmb/franz-go/pkg/sasl/scram"
)

type Auth struct {
	TLS           bool
	CAFile        string
	ClientCert    string
	ClientKey     string
	SASLMechanism string
	Username      string
	Password      string
}

type CaptureOptions struct {
	Brokers        []string
	Topics         []string
	OutputPath     string
	MaxMessages    int
	FromBeginning  bool
	Privacy        *privacy.Policy
	AllowSensitive bool
	Direction      string
	Auth           Auth
}

type CaptureResult struct {
	Captured int
	Topics   map[string]int
}

var kafkaTopicPattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

func validateTopic(topic string) error {
	if len(topic) == 0 || len(topic) > 249 || topic == "." || topic == ".." || !kafkaTopicPattern.MatchString(topic) {
		return fmt.Errorf("invalid Kafka topic %q", topic)
	}
	return nil
}

func Capture(ctx context.Context, opts CaptureOptions) (CaptureResult, error) {
	if len(opts.Brokers) == 0 || len(opts.Topics) == 0 || opts.OutputPath == "" {
		return CaptureResult{}, fmt.Errorf("brokers, topics, and output path are required")
	}
	for _, topic := range opts.Topics {
		if err := validateTopic(topic); err != nil {
			return CaptureResult{}, err
		}
	}
	if opts.Privacy == nil && !opts.AllowSensitive {
		return CaptureResult{}, fmt.Errorf("Kafka capture requires a privacy policy or explicit sensitive-data opt-in")
	}
	if opts.Privacy != nil && !opts.Privacy.CaptureBodies && !opts.AllowSensitive {
		return CaptureResult{}, fmt.Errorf("Kafka capture requires capture_bodies: true for replayable payloads or explicit sensitive-data opt-in")
	}
	if opts.MaxMessages < 0 {
		return CaptureResult{}, fmt.Errorf("maximum messages must be non-negative")
	}
	if opts.Direction == "" {
		opts.Direction = "publish"
	}
	if opts.Direction != "publish" && opts.Direction != "consume" {
		return CaptureResult{}, fmt.Errorf("Kafka direction must be publish or consume")
	}
	clientOptions, err := clientOptions(opts.Brokers, opts.Auth)
	if err != nil {
		return CaptureResult{}, err
	}
	clientOptions = append(clientOptions, kgo.ConsumeTopics(opts.Topics...))
	if opts.FromBeginning {
		clientOptions = append(clientOptions, kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()))
	}
	client, err := kgo.NewClient(clientOptions...)
	if err != nil {
		return CaptureResult{}, err
	}
	defer client.Close()
	if err := client.Ping(ctx); err != nil {
		return CaptureResult{}, fmt.Errorf("connect to Kafka: %w", err)
	}
	logger, err := message.NewLogger(opts.OutputPath)
	if err != nil {
		return CaptureResult{}, err
	}
	defer logger.Close()
	result := CaptureResult{Topics: make(map[string]int)}
	for {
		fetches := client.PollFetches(ctx)
		if ctx.Err() != nil {
			if opts.MaxMessages > 0 && result.Captured < opts.MaxMessages {
				return result, fmt.Errorf("capture stopped after %d of %d requested messages: %w", result.Captured, opts.MaxMessages, ctx.Err())
			}
			return result, nil
		}
		if fetchErrors := fetches.Errors(); len(fetchErrors) > 0 {
			return result, fmt.Errorf("Kafka fetch: %v", fetchErrors[0].Err)
		}
		for _, captured := range fetches.Records() {
			headers := make([]privacy.MessageHeader, 0, len(captured.Headers))
			for _, header := range captured.Headers {
				headers = append(headers, privacy.MessageHeader{Name: header.Key, Value: append([]byte(nil), header.Value...)})
			}
			key := append([]byte(nil), captured.Key...)
			payload := append([]byte(nil), captured.Value...)
			redacted := false
			if opts.Privacy != nil {
				key, headers, payload, err = opts.Privacy.ApplyMessage(key, headers, payload)
				if err != nil {
					return result, fmt.Errorf("sanitize Kafka message %s/%d/%d: %w", captured.Topic, captured.Partition, captured.Offset, err)
				}
				redacted = true
			}
			messageHeaders := make([]message.BinaryHeader, 0, len(headers))
			for _, header := range headers {
				messageHeaders = append(messageHeaders, message.BinaryHeader{Key: header.Name, Value: header.Value})
			}
			record := message.NewWithHeaders(captured.Topic, key, payload, messageHeaders)
			record.Broker = strings.Join(opts.Brokers, ",")
			record.Direction = opts.Direction
			record.Timestamp = captured.Timestamp.UTC()
			record.Partition = captured.Partition
			record.Offset = captured.Offset
			record.Redacted = redacted
			record.Schema = schemaName(headers)
			if err := logger.Write(record); err != nil {
				return result, err
			}
			result.Captured++
			result.Topics[captured.Topic]++
			if opts.MaxMessages > 0 && result.Captured >= opts.MaxMessages {
				return result, nil
			}
		}
	}
}

type Faults struct {
	Delay          time.Duration
	DropEvery      int
	DuplicateEvery int
	PoisonEvery    int
	ReorderWindow  int
}

type PlannedRecord struct {
	Record         message.Record
	OriginalIndex  int
	DuplicateIndex int
	Poisoned       bool
}

func Plan(records []message.Record, faults Faults) ([]PlannedRecord, error) {
	if faults.Delay < 0 || faults.DropEvery < 0 || faults.DuplicateEvery < 0 || faults.PoisonEvery < 0 || faults.ReorderWindow < 0 {
		return nil, fmt.Errorf("Kafka fault values must be non-negative")
	}
	ordered := append([]message.Record(nil), records...)
	if faults.ReorderWindow > 1 {
		for start := 0; start < len(ordered); start += faults.ReorderWindow {
			end := start + faults.ReorderWindow
			if end > len(ordered) {
				end = len(ordered)
			}
			for left, right := start, end-1; left < right; left, right = left+1, right-1 {
				ordered[left], ordered[right] = ordered[right], ordered[left]
			}
		}
	}
	indexByID := make(map[string]int, len(records))
	for index, record := range records {
		indexByID[record.ID] = index
	}
	var planned []PlannedRecord
	for orderedIndex, record := range ordered {
		position := orderedIndex + 1
		if faults.DropEvery > 0 && position%faults.DropEvery == 0 {
			continue
		}
		entry := PlannedRecord{Record: record, OriginalIndex: indexByID[record.ID]}
		if faults.PoisonEvery > 0 && position%faults.PoisonEvery == 0 {
			entry.Record.PayloadB64 = base64.StdEncoding.EncodeToString([]byte("{"))
			entry.Record.PayloadSHA256 = message.SHA256([]byte("{"))
			entry.Poisoned = true
		}
		planned = append(planned, entry)
		if faults.DuplicateEvery > 0 && position%faults.DuplicateEvery == 0 {
			duplicate := entry
			duplicate.DuplicateIndex = 1
			planned = append(planned, duplicate)
		}
	}
	return planned, nil
}

type ReplayOptions struct {
	Brokers     []string
	InputPath   string
	AsyncAPI    string
	TimeScale   float64
	Faults      Faults
	Auth        Auth
	TopicPrefix string
}

type ReplayResult struct {
	Loaded      int
	Produced    int
	Dropped     int
	Duplicated  int
	Poisoned    int
	Findings    []reporting.Finding
	Fingerprint string
}

func Replay(ctx context.Context, opts ReplayOptions) (ReplayResult, error) {
	if len(opts.Brokers) == 0 || opts.InputPath == "" {
		return ReplayResult{}, fmt.Errorf("brokers and input path are required")
	}
	if math.IsNaN(opts.TimeScale) || math.IsInf(opts.TimeScale, 0) || opts.TimeScale < 0 {
		return ReplayResult{}, fmt.Errorf("time scale must be non-negative")
	}
	records, err := message.Load(opts.InputPath)
	if err != nil {
		return ReplayResult{}, err
	}
	result := ReplayResult{Loaded: len(records)}
	if opts.AsyncAPI != "" {
		validator, err := asyncapi.Load(opts.AsyncAPI)
		if err != nil {
			return result, err
		}
		result.Findings = validator.Validate(records)
		if len(result.Findings) > 0 {
			return result, fmt.Errorf("AsyncAPI validation failed with %d finding(s)", len(result.Findings))
		}
	}
	planned, err := Plan(records, opts.Faults)
	if err != nil {
		return result, err
	}
	for _, entry := range planned {
		if err := validateTopic(opts.TopicPrefix + entry.Record.Topic); err != nil {
			return result, err
		}
	}
	result.Dropped = len(records) - uniquePlanned(planned)
	result.Duplicated = len(planned) - uniquePlanned(planned)
	for _, record := range planned {
		if record.Poisoned {
			result.Poisoned++
		}
	}
	clientOptions, err := clientOptions(opts.Brokers, opts.Auth)
	if err != nil {
		return result, err
	}
	clientOptions = append(clientOptions, kgo.RecordPartitioner(kgo.ManualPartitioner()))
	client, err := kgo.NewClient(clientOptions...)
	if err != nil {
		return result, err
	}
	defer client.Close()
	if err := client.Ping(ctx); err != nil {
		return result, fmt.Errorf("connect to Kafka: %w", err)
	}
	hashParts := make([]string, 0, len(planned))
	var previous time.Time
	for _, entry := range planned {
		if !previous.IsZero() && opts.TimeScale > 0 {
			gap := entry.Record.Timestamp.Sub(previous)
			if gap > 0 {
				timer := time.NewTimer(time.Duration(float64(gap) * opts.TimeScale))
				select {
				case <-ctx.Done():
					timer.Stop()
					return result, ctx.Err()
				case <-timer.C:
				}
			}
		}
		if opts.Faults.Delay > 0 {
			timer := time.NewTimer(opts.Faults.Delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return result, ctx.Err()
			case <-timer.C:
			}
		}
		key, err := entry.Record.Key()
		if err != nil {
			return result, err
		}
		payload, err := entry.Record.Payload()
		if err != nil {
			return result, err
		}
		headers, err := entry.Record.HeaderValues()
		if err != nil {
			return result, err
		}
		kafkaHeaders := make([]kgo.RecordHeader, 0, len(headers))
		for _, header := range headers {
			kafkaHeaders = append(kafkaHeaders, kgo.RecordHeader{Key: header.Key, Value: header.Value})
		}
		topic := opts.TopicPrefix + entry.Record.Topic
		outbound := &kgo.Record{
			Topic: topic, Partition: entry.Record.Partition, Key: key, Value: payload,
			Headers: kafkaHeaders, Timestamp: entry.Record.Timestamp,
		}
		if outbound.Partition < 0 {
			outbound.Partition = 0
		}
		if err := client.ProduceSync(ctx, outbound).FirstErr(); err != nil {
			return result, fmt.Errorf("produce %s partition %d: %w", topic, outbound.Partition, err)
		}
		result.Produced++
		fingerprintValue := struct {
			Topic          string
			Partition      int32
			KeyB64         string
			Headers        []message.Header
			PayloadSHA256  string
			DuplicateIndex int
			Poisoned       bool
		}{topic, outbound.Partition, entry.Record.KeyB64, entry.Record.Headers, entry.Record.PayloadSHA256, entry.DuplicateIndex, entry.Poisoned}
		fingerprintJSON, _ := json.Marshal(fingerprintValue)
		hashParts = append(hashParts, string(fingerprintJSON))
		previous = entry.Record.Timestamp
	}
	result.Fingerprint = message.SHA256([]byte(strings.Join(hashParts, "\n")))
	return result, nil
}

func uniquePlanned(records []PlannedRecord) int {
	seen := make(map[int]bool)
	for _, record := range records {
		seen[record.OriginalIndex] = true
	}
	return len(seen)
}

func clientOptions(brokers []string, auth Auth) ([]kgo.Opt, error) {
	for _, broker := range brokers {
		if strings.TrimSpace(broker) == "" {
			return nil, fmt.Errorf("Kafka broker address cannot be empty")
		}
	}
	mechanism := strings.ToLower(auth.SASLMechanism)
	if mechanism == "" && (auth.Username != "" || auth.Password != "") {
		return nil, fmt.Errorf("Kafka SASL credentials require a mechanism")
	}
	if mechanism != "" && (auth.Username == "" || auth.Password == "") {
		return nil, fmt.Errorf("Kafka SASL requires a username and password")
	}
	options := []kgo.Opt{kgo.SeedBrokers(brokers...), kgo.ClientID("infernosim")}
	if auth.TLS || auth.CAFile != "" || auth.ClientCert != "" || auth.ClientKey != "" {
		tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12}
		if auth.CAFile != "" {
			pem, err := os.ReadFile(auth.CAFile)
			if err != nil {
				return nil, fmt.Errorf("read Kafka CA: %w", err)
			}
			pool, err := x509.SystemCertPool()
			if err != nil || pool == nil {
				pool = x509.NewCertPool()
			}
			if !pool.AppendCertsFromPEM(pem) {
				return nil, fmt.Errorf("Kafka CA file contains no certificates")
			}
			tlsConfig.RootCAs = pool
		}
		if (auth.ClientCert == "") != (auth.ClientKey == "") {
			return nil, fmt.Errorf("Kafka client certificate and key must be configured together")
		}
		if auth.ClientCert != "" {
			certificate, err := tls.LoadX509KeyPair(auth.ClientCert, auth.ClientKey)
			if err != nil {
				return nil, fmt.Errorf("load Kafka client certificate: %w", err)
			}
			tlsConfig.Certificates = []tls.Certificate{certificate}
		}
		options = append(options, kgo.DialTLSConfig(tlsConfig))
	}
	switch mechanism {
	case "":
	case "plain":
		options = append(options, kgo.SASL(plain.Auth{User: auth.Username, Pass: auth.Password}.AsMechanism()))
	case "scram-sha-256":
		options = append(options, kgo.SASL(scram.Auth{User: auth.Username, Pass: auth.Password}.AsSha256Mechanism()))
	case "scram-sha-512":
		options = append(options, kgo.SASL(scram.Auth{User: auth.Username, Pass: auth.Password}.AsSha512Mechanism()))
	default:
		return nil, fmt.Errorf("unsupported Kafka SASL mechanism %q", auth.SASLMechanism)
	}
	return options, nil
}

func schemaName(headers []privacy.MessageHeader) string {
	for _, wanted := range []string{"message-type", "ce-type", "schema-name"} {
		for _, header := range headers {
			if strings.EqualFold(header.Name, wanted) {
				return string(header.Value)
			}
		}
	}
	return ""
}
