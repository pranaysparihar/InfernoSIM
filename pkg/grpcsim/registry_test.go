package grpcsim

import (
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	pb "infernosim/examples/grpcapp/echo"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/types/descriptorpb"
)

func testRegistry(t *testing.T) *Registry {
	t.Helper()
	registry, err := Load(Config{
		ProtoFiles:  []string{filepath.Join("..", "..", "examples", "grpcapp", "echo", "echo.proto")},
		ImportPaths: []string{filepath.Join("..", "..", "examples", "grpcapp", "echo")},
	})
	if err != nil {
		t.Fatal(err)
	}
	return registry
}

func TestRegistryDecodesAndSynthesizesUnaryMessages(t *testing.T) {
	registry := testRegistry(t)
	payload, err := proto.Marshal(&pb.EchoRequest{Message: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	frame := make([]byte, 5+len(payload))
	binary.BigEndian.PutUint32(frame[1:5], uint32(len(payload)))
	copy(frame[5:], payload)

	value, err := registry.DecodeRequest("/echo.EchoService/Echo", frame)
	if err != nil {
		t.Fatal(err)
	}
	object := value.(map[string]any)
	if object["message"] != "hello" {
		t.Fatalf("decoded=%#v", object)
	}

	frames, err := registry.EncodeResponse("/echo.EchoService/Echo", []string{`{"message":"generated"}`})
	if err != nil {
		t.Fatal(err)
	}
	if len(frames) != 1 {
		t.Fatalf("frames=%d", len(frames))
	}
	responsePayload := frames[0][5:]
	var response pb.EchoResponse
	if err := proto.Unmarshal(responsePayload, &response); err != nil {
		t.Fatal(err)
	}
	if response.Message != "generated" {
		t.Fatalf("response=%q", response.Message)
	}
}

func TestRegistryEncodesStreamingFramesAndGeneratesExample(t *testing.T) {
	registry := testRegistry(t)
	frames, err := registry.EncodeResponse("/echo.EchoService/Echo", []string{
		`{"message":"one"}`,
		`{"message":"two"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(frames) != 2 {
		t.Fatalf("frames=%d", len(frames))
	}
	example, err := registry.ResponseExample("/echo.EchoService/Echo")
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err := json.Unmarshal([]byte(example), &value); err != nil {
		t.Fatal(err)
	}
	if _, ok := value["message"]; !ok {
		t.Fatalf("example=%s", example)
	}
}

func TestRegistryRejectsMalformedOrCompressedFrames(t *testing.T) {
	registry := testRegistry(t)
	if _, err := registry.DecodeRequest("/echo.EchoService/Echo", []byte{1, 0, 0, 0, 0}); err == nil {
		t.Fatal("expected compressed frame rejection")
	}
	if _, err := registry.DecodeRequest("/echo.EchoService/Echo", []byte{0, 0, 0}); err == nil {
		t.Fatal("expected truncated frame rejection")
	}
}

func TestRegistryLoadsBinaryDescriptorSet(t *testing.T) {
	set := &descriptorpb.FileDescriptorSet{
		File: []*descriptorpb.FileDescriptorProto{
			protodesc.ToFileDescriptorProto(pb.File_examples_grpcapp_echo_echo_proto),
		},
	}
	data, err := proto.Marshal(set)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "echo.binpb")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	registry, err := Load(Config{DescriptorSets: []string{path}})
	if err != nil {
		t.Fatal(err)
	}
	methods := registry.Methods()
	if len(methods) != 4 {
		t.Fatalf("methods=%#v", methods)
	}
}

func FuzzSplitFrames(f *testing.F) {
	f.Add([]byte{0, 0, 0, 0, 0})
	f.Add([]byte{1, 0, 0, 0, 0})
	f.Fuzz(func(t *testing.T, input []byte) {
		_, _ = splitFrames(input)
	})
}

func BenchmarkDecodeRequest(b *testing.B) {
	registry, err := Load(Config{
		ProtoFiles:  []string{filepath.Join("..", "..", "examples", "grpcapp", "echo", "echo.proto")},
		ImportPaths: []string{filepath.Join("..", "..", "examples", "grpcapp", "echo")},
	})
	if err != nil {
		b.Fatal(err)
	}
	payload, _ := proto.Marshal(&pb.EchoRequest{Message: "benchmark"})
	frame := make([]byte, 5+len(payload))
	binary.BigEndian.PutUint32(frame[1:5], uint32(len(payload)))
	copy(frame[5:], payload)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := registry.DecodeRequest("/echo.EchoService/Echo", frame); err != nil {
			b.Fatal(err)
		}
	}
}
