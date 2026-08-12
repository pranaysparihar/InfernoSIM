package heal

import (
	"encoding/base64"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"infernosim/pkg/event"
	"infernosim/pkg/replaydriver"
)

func TestRunProposesNarrowPatternsAndProtectsBusinessFields(t *testing.T) {
	dir := t.TempDir()
	writeIncident(t, dir, []event.Event{
		outbound(`{"request_id":"550e8400-e29b-41d4-a716-446655440000","amount":10}`, "one"),
		outbound(`{"request_id":"550e8400-e29b-41d4-a716-446655440001","amount":20}`, "two"),
		outbound(`{"request_id":"550e8400-e29b-41d4-a716-446655440002","amount":30}`, "three"),
	})
	output := filepath.Join(dir, "replay.proposed.yaml")
	result, err := Run(Options{IncidentDirs: []string{dir}, OutputPath: output, MinimumSamples: 3})
	if err != nil {
		t.Fatal(err)
	}
	if result.Accepted != 1 || result.Rejected != 1 || !result.ValidationPassed {
		t.Fatalf("result=%+v proposals=%+v", result, result.Proposals)
	}
	data, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, "$.request_id") || strings.Contains(text, "$.amount:") {
		t.Fatalf("unexpected proposed config:\n%s", text)
	}
	if _, err := replaydriver.LoadReplayConfig(output); err != nil {
		t.Fatalf("proposed config does not load: %v", err)
	}
	for _, proposal := range result.Proposals {
		if strings.Contains(proposal.Evidence, "550e") || strings.Contains(proposal.Evidence, "10") {
			t.Fatal("report leaked raw evidence")
		}
	}
}

func TestRunFailsClosedOnAmbiguousDifferentResponses(t *testing.T) {
	dir := t.TempDir()
	one := outbound(`{"request_id":"550e8400-e29b-41d4-a716-446655440000"}`, "one")
	two := outbound(`{"request_id":"550e8400-e29b-41d4-a716-446655440001"}`, "two")
	three := outbound(`{"request_id":"550e8400-e29b-41d4-a716-446655440002"}`, "three")
	one.Status, two.Status, three.Status = 200, 201, 202
	writeIncident(t, dir, []event.Event{one, two, three})
	output := filepath.Join(dir, "replay.proposed.yaml")
	result, err := Run(Options{IncidentDirs: []string{dir}, OutputPath: output})
	if err == nil || result.Ambiguities == 0 {
		t.Fatalf("expected ambiguity failure, result=%+v err=%v", result, err)
	}
	if _, statErr := os.Stat(output); !os.IsNotExist(statErr) {
		t.Fatal("ambiguous configuration was written")
	}
	if _, statErr := os.Stat(filepath.Join(dir, "reports", "infernosim-healing-report.json")); statErr != nil {
		t.Fatal("failure report was not written")
	}
}

func TestGeneratedConfigIsDeterministic(t *testing.T) {
	dir := t.TempDir()
	writeIncident(t, dir, []event.Event{
		outbound(`{"created_at":"2026-01-01T01:02:03Z"}`, "same"),
		outbound(`{"created_at":"2026-01-02T01:02:03Z"}`, "same"),
		outbound(`{"created_at":"2026-01-03T01:02:03Z"}`, "same"),
	})
	one := filepath.Join(dir, "one.yaml")
	two := filepath.Join(dir, "two.yaml")
	if _, err := Run(Options{IncidentDirs: []string{dir}, OutputPath: one}); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(Options{IncidentDirs: []string{dir}, OutputPath: two}); err != nil {
		t.Fatal(err)
	}
	a, _ := os.ReadFile(one)
	b, _ := os.ReadFile(two)
	if string(a) != string(b) {
		t.Fatal("proposed config is not deterministic")
	}
}

func TestApplyBacksUpExistingConfig(t *testing.T) {
	dir := t.TempDir()
	writeIncident(t, dir, []event.Event{
		outbound(`{"created_at":"2026-01-01T01:02:03Z"}`, "same"),
		outbound(`{"created_at":"2026-01-02T01:02:03Z"}`, "same"),
		outbound(`{"created_at":"2026-01-03T01:02:03Z"}`, "same"),
	})
	config := filepath.Join(dir, "replay.yaml")
	if err := os.WriteFile(config, []byte("runs: 7\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := Run(Options{IncidentDirs: []string{dir}, ConfigPath: config, Apply: true})
	if err != nil {
		t.Fatal(err)
	}
	if result.AppliedPath != config || result.BackupPath != config+".bak" {
		t.Fatalf("result=%+v", result)
	}
	backup, _ := os.ReadFile(result.BackupPath)
	if string(backup) != "runs: 7\n" {
		t.Fatalf("backup=%q", backup)
	}
	loaded, err := replaydriver.LoadReplayConfig(config)
	if err != nil || loaded.Runs != 7 || len(loaded.Matching.Rules) != 1 {
		t.Fatalf("applied config=%+v err=%v", loaded, err)
	}
}

func TestRunRefusesProposedOverwriteUnlessForced(t *testing.T) {
	dir := t.TempDir()
	writeIncident(t, dir, []event.Event{
		outbound(`{"created_at":"2026-01-01T01:02:03Z"}`, "same"),
		outbound(`{"created_at":"2026-01-02T01:02:03Z"}`, "same"),
		outbound(`{"created_at":"2026-01-03T01:02:03Z"}`, "same"),
	})
	output := filepath.Join(dir, "proposal.yaml")
	if err := os.WriteFile(output, []byte("user edit"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(Options{IncidentDirs: []string{dir}, OutputPath: output}); err == nil {
		t.Fatal("expected overwrite refusal")
	}
	if _, err := Run(Options{IncidentDirs: []string{dir}, OutputPath: output, Force: true}); err != nil {
		t.Fatal(err)
	}
}

func TestRunDoesNotRelaxArbitraryIdentityUUID(t *testing.T) {
	dir := t.TempDir()
	writeIncident(t, dir, []event.Event{
		outbound(`{"user_id":"550e8400-e29b-41d4-a716-446655440000"}`, "same"),
		outbound(`{"user_id":"550e8400-e29b-41d4-a716-446655440001"}`, "same"),
		outbound(`{"user_id":"550e8400-e29b-41d4-a716-446655440002"}`, "same"),
	})
	result, err := Run(Options{IncidentDirs: []string{dir}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Accepted != 0 || result.Rejected != 1 {
		t.Fatalf("identity UUID must remain exact: %+v", result)
	}
}

func TestRunRemovesStaleManagedRules(t *testing.T) {
	dir := t.TempDir()
	writeIncident(t, dir, []event.Event{
		outbound(`{"user_id":"550e8400-e29b-41d4-a716-446655440000"}`, "same"),
		outbound(`{"user_id":"550e8400-e29b-41d4-a716-446655440001"}`, "same"),
		outbound(`{"user_id":"550e8400-e29b-41d4-a716-446655440002"}`, "same"),
	})
	config := filepath.Join(dir, "replay.yaml")
	if err := os.WriteFile(config, []byte(`matching:
  rules:
    - name: heal-stale
      methods: [POST]
      host_regex: '^payments\\.example\\.test$'
      path_regex: '^/charge$'
      jsonpath_regex:
        $.user_id: '.*'
`), 0o600); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(dir, "proposed.yaml")
	if _, err := Run(Options{IncidentDirs: []string{dir}, ConfigPath: config, OutputPath: output}); err != nil {
		t.Fatal(err)
	}
	loaded, err := replaydriver.LoadReplayConfig(output)
	if err != nil {
		t.Fatal(err)
	}
	for _, rule := range loaded.Matching.Rules {
		if strings.HasPrefix(rule.Name, "heal-") {
			t.Fatalf("stale managed rule survived regeneration: %+v", rule)
		}
	}
}

func TestRunRejectsNonFiniteConfidence(t *testing.T) {
	dir := t.TempDir()
	writeIncident(t, dir, nil)
	if _, err := Run(Options{IncidentDirs: []string{dir}, MinimumScore: math.NaN()}); err == nil {
		t.Fatal("expected NaN confidence rejection")
	}
}

func outbound(body, response string) event.Event {
	encodedBody := base64.StdEncoding.EncodeToString([]byte(body))
	encodedResponse := base64.StdEncoding.EncodeToString([]byte(response))
	return event.Event{
		ID: "id", Type: "OutboundCall", Timestamp: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		Method: "POST", URL: "https://payments.example.test/charge", Headers: map[string][]string{"Content-Type": {"application/json"}},
		BodyB64: encodedBody, BodySha256: hashBytes([]byte(body)), Status: 200, ResponseCaptured: true,
		ResponseBodyB64: encodedResponse, ResponseBodySha256: hashBytes([]byte(response)),
	}
}

func writeIncident(t *testing.T, dir string, events []event.Event) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "inbound.log"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(filepath.Join(dir, "outbound.log"), os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	encoder := json.NewEncoder(file)
	for _, captured := range events {
		if err := encoder.Encode(captured); err != nil {
			t.Fatal(err)
		}
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func FuzzFlattenJSON(f *testing.F) {
	for _, seed := range []string{`{}`, `{"request_id":"value"}`, `[{"at":"2026-01-01T00:00:00Z"}]`, `{`} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, input string) {
		first := flattenJSON([]byte(input))
		second := flattenJSON([]byte(input))
		if len(first) != len(second) {
			t.Fatal("flatten result is not deterministic")
		}
		for key, value := range first {
			if second[key] != value {
				t.Fatal("flatten result is not deterministic")
			}
		}
	})
}
