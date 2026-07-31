package generator

import (
	"path/filepath"
	"strings"
	"testing"

	"infernosim/pkg/grpcsim"
	"infernosim/pkg/replaydriver"
)

func TestGenerateOpenAPIConfigLoads(t *testing.T) {
	data, err := FromOpenAPI(filepath.Join("..", "..", "examples", "openapi.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "replay.yaml")
	if err := writeTestFile(path, data); err != nil {
		t.Fatal(err)
	}
	config, err := replaydriver.LoadReplayConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(config.Scenarios) == 0 || len(config.Scenarios[0].Steps) == 0 {
		t.Fatal("generated OpenAPI config contains no scenario steps")
	}
}

func TestGenerateProtobufConfigLoads(t *testing.T) {
	protoPath, err := filepath.Abs(filepath.Join("..", "..", "examples", "grpcapp", "echo", "echo.proto"))
	if err != nil {
		t.Fatal(err)
	}
	data, err := FromProtobuf(grpcsim.Config{
		ProtoFiles:  []string{protoPath},
		ImportPaths: []string{filepath.Dir(protoPath)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "protobuf_json") || !strings.Contains(string(data), "grpc_method") {
		t.Fatalf("generated config:\n%s", data)
	}
	path := filepath.Join(t.TempDir(), "replay.yaml")
	if err := writeTestFile(path, data); err != nil {
		t.Fatal(err)
	}
	if _, err := replaydriver.LoadReplayConfig(path); err != nil {
		t.Fatal(err)
	}
}
