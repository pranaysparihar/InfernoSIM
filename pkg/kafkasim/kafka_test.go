package kafkasim

import (
	"context"
	"math"
	"path/filepath"
	"testing"
	"time"

	"infernosim/pkg/message"
	"infernosim/pkg/privacy"
)

func TestPlanFaultsIsDeterministic(t *testing.T) {
	var records []message.Record
	for index := 0; index < 6; index++ {
		record := message.New("topic", []byte{byte(index)}, []byte{byte(index)}, nil)
		record.ID = string(rune('a' + index))
		record.Timestamp = time.Unix(int64(index), 0).UTC()
		records = append(records, record)
	}
	faults := Faults{DropEvery: 3, DuplicateEvery: 2, PoisonEvery: 4, ReorderWindow: 2}
	one, err := Plan(records, faults)
	if err != nil {
		t.Fatal(err)
	}
	two, err := Plan(records, faults)
	if err != nil {
		t.Fatal(err)
	}
	if len(one) != 6 || len(two) != len(one) {
		t.Fatalf("planned lengths one=%d two=%d", len(one), len(two))
	}
	for index := range one {
		if one[index].Record.ID != two[index].Record.ID || one[index].DuplicateIndex != two[index].DuplicateIndex || one[index].Poisoned != two[index].Poisoned {
			t.Fatalf("plan differs at %d", index)
		}
	}
	if one[0].Record.ID != "b" || one[1].Record.ID != "a" {
		t.Fatalf("reorder was not applied: %+v", one)
	}
}

func TestPlanRejectsNegativeFaults(t *testing.T) {
	if _, err := Plan(nil, Faults{DropEvery: -1}); err == nil {
		t.Fatal("expected validation failure")
	}
}

func TestCaptureRequiresReplayablePrivacyPolicy(t *testing.T) {
	_, err := Capture(context.Background(), CaptureOptions{
		Brokers: []string{"127.0.0.1:9092"}, Topics: []string{"topic"},
		OutputPath: filepath.Join(t.TempDir(), "messages.log"), Privacy: &privacy.Policy{Version: 1},
	})
	if err == nil {
		t.Fatal("expected capture_bodies safety failure")
	}
}

func TestClientOptionsRejectIncompleteSASL(t *testing.T) {
	if _, err := clientOptions([]string{"127.0.0.1:9092"}, Auth{Username: "user", Password: "secret"}); err == nil {
		t.Fatal("expected mechanism requirement")
	}
	if _, err := clientOptions([]string{"127.0.0.1:9092"}, Auth{SASLMechanism: "plain", Username: "user"}); err == nil {
		t.Fatal("expected password requirement")
	}
}

func TestKafkaTopicAndTimeScaleValidation(t *testing.T) {
	if err := validateTopic("invalid/topic"); err == nil {
		t.Fatal("expected invalid topic rejection")
	}
	path := filepath.Join(t.TempDir(), "messages.log")
	logger, err := message.NewLogger(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := logger.Write(message.New("topic", nil, []byte(`{}`), nil)); err != nil {
		t.Fatal(err)
	}
	_ = logger.Close()
	if _, err := Replay(context.Background(), ReplayOptions{Brokers: []string{"127.0.0.1:9092"}, InputPath: path, TimeScale: math.NaN()}); err == nil {
		t.Fatal("expected non-finite time scale rejection")
	}
	if _, err := Replay(context.Background(), ReplayOptions{Brokers: []string{"127.0.0.1:9092"}, InputPath: path, TopicPrefix: "bad/"}); err == nil {
		t.Fatal("expected invalid prefixed topic rejection")
	}
}
