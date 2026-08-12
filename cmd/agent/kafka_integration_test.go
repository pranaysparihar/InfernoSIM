package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/twmb/franz-go/pkg/kfake"
	"github.com/twmb/franz-go/pkg/kgo"
)

func TestKafkaCLIWireProtocolCaptureValidateReplay(t *testing.T) {
	cluster, err := kfake.NewCluster(
		kfake.NumBrokers(1),
		kfake.SeedTopics(1, "payment.authorized", "ci.payment.authorized"),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cluster.Close)
	brokers := cluster.ListenAddrs()
	if len(brokers) != 1 {
		t.Fatalf("brokers=%v", brokers)
	}

	dir := t.TempDir()
	incident := filepath.Join(dir, "incident")
	if err := os.MkdirAll(incident, 0o700); err != nil {
		t.Fatal(err)
	}
	messageLog := filepath.Join(incident, "messages.log")
	captureResult := make(chan int, 1)
	go func() {
		captureResult <- runKafkaCapture([]string{
			"--brokers", brokers[0], "--topics", "payment.authorized",
			"--out", messageLog, "--max-messages", "1", "--from-beginning",
			"--capture-sensitive-data",
		})
	}()

	producer, err := kgo.NewClient(kgo.SeedBrokers(brokers...))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(producer.Close)
	produceContext, produceCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer produceCancel()
	payload := []byte(`{"payment_id":"pay_42","status":"authorized"}`)
	if err := producer.ProduceSync(produceContext, &kgo.Record{Topic: "payment.authorized", Value: payload}).FirstErr(); err != nil {
		t.Fatal(err)
	}
	select {
	case code := <-captureResult:
		if code != 0 {
			t.Fatalf("kafka capture exit code=%d", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Kafka CLI capture timed out")
	}

	asyncAPIPath := filepath.Join(dir, "asyncapi.yaml")
	asyncAPI := `asyncapi: 3.0.0
channels:
  authorized:
    address: payment.authorized
    messages:
      PaymentAuthorized:
        payload:
          type: object
          additionalProperties: false
          required: [payment_id, status]
          properties:
            payment_id: {type: string}
            status: {type: string, enum: [authorized]}
`
	if err := os.WriteFile(asyncAPIPath, []byte(asyncAPI), 0o600); err != nil {
		t.Fatal(err)
	}
	reportDir := filepath.Join(dir, "reports")
	if code := runKafkaValidate([]string{incident, "--asyncapi", asyncAPIPath, "--report-dir", reportDir}); code != 0 {
		t.Fatalf("kafka validate exit code=%d", code)
	}
	if code := runKafkaReplay([]string{
		incident, "--brokers", brokers[0], "--asyncapi", asyncAPIPath,
		"--topic-prefix", "ci.", "--report-dir", reportDir, "--timeout", "10s",
	}); code != 0 {
		t.Fatalf("kafka replay exit code=%d", code)
	}

	consumer, err := kgo.NewClient(
		kgo.SeedBrokers(brokers...),
		kgo.ConsumeTopics("ci.payment.authorized"),
		kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(consumer.Close)
	consumeContext, consumeCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer consumeCancel()
	fetches := consumer.PollFetches(consumeContext)
	if errors := fetches.Errors(); len(errors) > 0 {
		t.Fatal(errors[0])
	}
	records := fetches.Records()
	if len(records) != 1 || string(records[0].Value) != string(payload) {
		t.Fatalf("replayed records=%+v", records)
	}
	for _, name := range []string{
		"infernosim-report.junit.xml", "infernosim-report.sarif",
		"infernosim-report.html", "infernosim-kafka-proof.json",
	} {
		if info, err := os.Stat(filepath.Join(reportDir, name)); err != nil || info.Size() == 0 {
			t.Fatalf("report %s missing or empty: %v", name, err)
		}
	}
}
