package kafkasim

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"infernosim/pkg/message"

	"github.com/twmb/franz-go/pkg/kfake"
	"github.com/twmb/franz-go/pkg/kgo"
)

func TestCaptureValidateAndReplayOverKafkaProtocol(t *testing.T) {
	cluster, err := kfake.NewCluster(
		kfake.NumBrokers(1),
		kfake.SeedTopics(1, "payment.authorized", "ci.payment.authorized"),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cluster.Close)
	brokers := cluster.ListenAddrs()

	inputPath := filepath.Join(t.TempDir(), "messages.log")
	captureContext, captureCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer captureCancel()
	type captureResponse struct {
		result CaptureResult
		err    error
	}
	captured := make(chan captureResponse, 1)
	go func() {
		result, err := Capture(captureContext, CaptureOptions{
			Brokers: brokers, Topics: []string{"payment.authorized"}, OutputPath: inputPath,
			MaxMessages: 1, FromBeginning: true, AllowSensitive: true,
		})
		captured <- captureResponse{result: result, err: err}
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
	response := <-captured
	if response.err != nil || response.result.Captured != 1 {
		t.Fatalf("capture result=%+v err=%v", response.result, response.err)
	}
	records, err := message.Load(inputPath)
	if err != nil || len(records) != 1 || records[0].Topic != "payment.authorized" {
		t.Fatalf("records=%+v err=%v", records, err)
	}

	specPath := filepath.Join(t.TempDir(), "asyncapi.yaml")
	spec := `asyncapi: 3.0.0
channels:
  authorized:
    address: payment.authorized
    messages:
      PaymentAuthorized:
        name: PaymentAuthorized
        payload:
          type: object
          required: [payment_id, status]
          properties:
            payment_id: {type: string}
            status: {type: string, enum: [authorized]}
`
	if err := os.WriteFile(specPath, []byte(spec), 0o600); err != nil {
		t.Fatal(err)
	}
	replayContext, replayCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer replayCancel()
	replayed, err := Replay(replayContext, ReplayOptions{
		Brokers: brokers, InputPath: inputPath, AsyncAPI: specPath, TopicPrefix: "ci.",
	})
	if err != nil || replayed.Produced != 1 || replayed.Fingerprint == "" {
		t.Fatalf("replay result=%+v err=%v", replayed, err)
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
	outputRecords := fetches.Records()
	if len(outputRecords) != 1 || string(outputRecords[0].Value) != string(payload) {
		t.Fatalf("replayed records=%+v", outputRecords)
	}
}
