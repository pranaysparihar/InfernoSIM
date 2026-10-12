package toolstream

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var fixtures = map[string]string{"responses.sse": "openai", "chat.sse": "openai", "anthropic.sse": "anthropic", "ollama.ndjson": "ollama"}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestInterleavedAssemblyAndIndividualMutation(t *testing.T) {
	for name, provider := range fixtures {
		t.Run(name, func(t *testing.T) {
			original := fixture(t, name)
			s, err := Parse(provider, original)
			if err != nil {
				t.Fatal(err)
			}
			if len(s.Calls) != 2 {
				t.Fatalf("calls: %+v", s.Calls)
			}
			unchanged, err := s.Bytes()
			if err != nil || !bytes.Equal(unchanged, original) {
				t.Fatal("unchanged stream was rewritten")
			}
			first := append([]byte(nil), s.Calls[0].Arguments...)
			target := s.Calls[1].ID
			if err = s.ReplaceArguments(target, func(b []byte) ([]byte, error) {
				v, err := decode(b)
				if err != nil {
					return nil, err
				}
				object(v)["amount"] = json.Number("999")
				return json.Marshal(v)
			}); err != nil {
				t.Fatal(err)
			}
			changed, err := s.Bytes()
			if err != nil {
				t.Fatal(err)
			}
			again, err := Parse(provider, changed)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(again.Calls[0].Arguments, first) {
				t.Fatal("another tool's arguments changed")
			}
			v, _ := decode(again.Calls[1].Arguments)
			if object(v)["amount"] != json.Number("999") {
				t.Fatal("selected tool was not changed")
			}
			// Every frame without the targeted tool's argument locations is byte exact.
			before, _ := parseFrames(original, provider != "ollama")
			after, _ := parseFrames(changed, provider != "ollama")
			if len(before) != len(after) {
				t.Fatal("frame count changed")
			}
			for i, f := range s.frames {
				if !f.changed && !bytes.Equal(before[i].raw, after[i].raw) {
					t.Fatalf("unrelated frame %d changed", i)
				}
			}
		})
	}
}

func TestMalformedTruncatedAndDuplicateCompletionRejected(t *testing.T) {
	for name, provider := range fixtures {
		t.Run(name, func(t *testing.T) {
			b := fixture(t, name)
			frames, err := parseFrames(b, provider != "ollama")
			if err != nil {
				t.Fatal(err)
			}
			last := frames[len(frames)-1].raw
			for label, bad := range map[string][]byte{"missing_terminal": b[:len(b)-len(last)], "duplicate_terminal": append(append([]byte(nil), b...), last...), "truncated_frame": b[:len(b)-1], "malformed_json": bytes.Replace(b, []byte(`"amount\":20`), []byte(`"amount\":oops`), 1)} {
				if bytes.Equal(bad, b) {
					continue
				}
				if _, err := Parse(provider, bad); err == nil {
					t.Fatalf("accepted %s", label)
				}
			}
		})
	}
	responses := fixture(t, "responses.sse")
	for _, bad := range [][]byte{bytes.Replace(responses, []byte(`"item_id":"item_b"`), []byte(`"item_id":"missing"`), 1), bytes.Replace(responses, []byte(`"type":"response.function_call_arguments.done"`), []byte(`"type":"response.unsupported"`), 1)} {
		if _, err := Parse("openai", bad); err == nil {
			t.Fatal("accepted unsupported or mismatched stream")
		}
	}
}

func TestSSECRLFAndMultilinePayload(t *testing.T) {
	data := fixture(t, "chat.sse")
	data = bytes.Replace(data, []byte(`,"id":"chat_1"`), []byte(",\ndata: \"id\":\"chat_1\""), 1)
	data = bytes.ReplaceAll(data, []byte("\n"), []byte("\r\n"))
	s, err := Parse("openai", data)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.ReplaceArguments("call_b", func([]byte) ([]byte, error) { return []byte(`{"account":"B","amount":99}`), nil }); err != nil {
		t.Fatal(err)
	}
	result, err := s.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Parse("openai", result); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(result, []byte("\r\n")) {
		t.Fatal("line endings lost")
	}
}

func TestUnsupportedEventNeverPasses(t *testing.T) {
	for _, data := range []string{"data: {\"type\":\"new_tool_event\"}\n\n", "data: {bad}\n\n", "data: [DONE]\n\n"} {
		if _, err := Parse("openai", []byte(data)); err == nil {
			t.Fatal("accepted unsupported stream")
		}
	}
}

func TestAnthropicEmptyToolAndResponsesNonToolCompletion(t *testing.T) {
	data := []byte("data: {\"type\":\"message_start\",\"message\":{}}\n\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"empty\",\"name\":\"clock\",\"input\":{}}}\n\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"}}\n\ndata: {\"type\":\"message_stop\"}\n\n")
	s, err := Parse("anthropic", data)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Calls) != 1 || string(s.Calls[0].Arguments) != "{}" {
		t.Fatal("no-argument tool lost")
	}
	if err := s.ReplaceArguments("empty", func([]byte) ([]byte, error) { return []byte(`{"timezone":"UTC"}`), nil }); err != nil {
		t.Fatal(err)
	}
	mutated, _ := s.Bytes()
	if _, err := Parse("anthropic", mutated); err != nil {
		t.Fatal(err)
	}
	start := "data: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"id\":\"text\",\"type\":\"message\"}}\n\n"
	done := "data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"id\":\"text\",\"type\":\"message\"}}\n\n"
	end := "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"response_id\",\"status\":\"completed\",\"output\":[]}}\n\n"
	if _, err := Parse("openai", []byte(start+done+end)); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{start + done + done + end, start + end} {
		if _, err := Parse("openai", []byte(bad)); err == nil {
			t.Fatal("accepted missing/duplicate non-tool completion")
		}
	}
}

func FuzzProviderStreams(f *testing.F) {
	for name, provider := range fixtures {
		b, _ := os.ReadFile(filepath.Join("testdata", name))
		f.Add(provider, b)
	}
	f.Fuzz(func(t *testing.T, provider string, data []byte) {
		if len(data) > 1<<20 {
			return
		}
		s, err := Parse(provider, data)
		if err != nil {
			return
		}
		original, err := s.Bytes()
		if err != nil || !bytes.Equal(data, original) {
			t.Fatal("unchanged stream is not byte stable")
		}
		if len(s.Calls) > 0 {
			err = s.ReplaceArguments(s.Calls[0].ID, func([]byte) ([]byte, error) { return []byte(`{"fuzz":true}`), nil })
			if err != nil {
				t.Fatal(err)
			}
			b, err := s.Bytes()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := Parse(provider, b); err != nil {
				t.Fatal(err)
			}
		}
	})
}

func TestPerEnvelopeTarget(t *testing.T) {
	s, err := ParseEnvelope("openai", []byte(`{"output":[{"type":"function_call","call_id":"a","name":"one","arguments":"{\"value\":1}"},{"type":"function_call","call_id":"b","name":"two","arguments":"{\"value\":2}"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if err = s.ReplaceArguments("b", func([]byte) ([]byte, error) { return []byte(`{"value":3}`), nil }); err != nil {
		t.Fatal(err)
	}
	b, err := s.Bytes()
	if err != nil || !strings.Contains(string(b), `\"value\":3`) {
		t.Fatal(string(b), err)
	}
}
