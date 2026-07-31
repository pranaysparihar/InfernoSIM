package grpcsim

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/bufbuild/protocompile"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
)

const (
	maxFrameBytes   = 16 * 1024 * 1024
	maxStreamFrames = 1024
)

// Config identifies Protobuf schemas used for semantic gRPC matching and
// response synthesis. Paths are resolved relative to replay.yaml.
type Config struct {
	ProtoFiles     []string `yaml:"proto_files" json:"proto_files,omitempty"`
	DescriptorSets []string `yaml:"descriptor_sets" json:"descriptor_sets,omitempty"`
	ImportPaths    []string `yaml:"import_paths" json:"import_paths,omitempty"`
}

func (c Config) Empty() bool {
	return len(c.ProtoFiles) == 0 && len(c.DescriptorSets) == 0
}

func (c *Config) ResolvePaths(base string) {
	c.ProtoFiles = resolvePaths(base, c.ProtoFiles)
	c.DescriptorSets = resolvePaths(base, c.DescriptorSets)
	c.ImportPaths = resolvePaths(base, c.ImportPaths)
}

func resolvePaths(base string, paths []string) []string {
	out := make([]string, 0, len(paths))
	for _, path := range paths {
		if path != "" && !filepath.IsAbs(path) {
			path = filepath.Join(base, path)
		}
		out = append(out, filepath.Clean(path))
	}
	return out
}

type Registry struct {
	files    *protoregistry.Files
	methods  map[string]protoreflect.MethodDescriptor
	messages map[protoreflect.FullName]protoreflect.MessageDescriptor
}

type Method struct {
	Path            string `json:"path"`
	InputType       string `json:"input_type"`
	OutputType      string `json:"output_type"`
	ClientStreaming bool   `json:"client_streaming"`
	ServerStreaming bool   `json:"server_streaming"`
}

func Load(cfg Config) (*Registry, error) {
	files := new(protoregistry.Files)
	for _, descriptorPath := range cfg.DescriptorSets {
		data, err := os.ReadFile(descriptorPath)
		if err != nil {
			return nil, fmt.Errorf("read descriptor set %q: %w", descriptorPath, err)
		}
		var set descriptorpb.FileDescriptorSet
		if err := proto.Unmarshal(data, &set); err != nil {
			return nil, fmt.Errorf("decode descriptor set %q: %w", descriptorPath, err)
		}
		loaded, err := protodesc.NewFiles(&set)
		if err != nil {
			return nil, fmt.Errorf("link descriptor set %q: %w", descriptorPath, err)
		}
		if err := mergeFiles(files, loaded); err != nil {
			return nil, fmt.Errorf("register descriptor set %q: %w", descriptorPath, err)
		}
	}
	if len(cfg.ProtoFiles) > 0 {
		compiled, err := compileProtoFiles(cfg)
		if err != nil {
			return nil, err
		}
		visited := make(map[string]bool)
		for _, file := range compiled {
			if err := registerFileRecursive(files, file, visited); err != nil {
				return nil, fmt.Errorf("register compiled proto %q: %w", file.Path(), err)
			}
		}
	}
	registry := &Registry{
		files:    files,
		methods:  make(map[string]protoreflect.MethodDescriptor),
		messages: make(map[protoreflect.FullName]protoreflect.MessageDescriptor),
	}
	files.RangeFiles(func(file protoreflect.FileDescriptor) bool {
		indexFile(registry, file)
		return true
	})
	if len(registry.methods) == 0 {
		return nil, fmt.Errorf("Protobuf schemas contain no gRPC service methods")
	}
	return registry, nil
}

func compileProtoFiles(cfg Config) ([]protoreflect.FileDescriptor, error) {
	importPaths := append([]string(nil), cfg.ImportPaths...)
	var roots []string
	for _, path := range cfg.ProtoFiles {
		absolute, err := filepath.Abs(path)
		if err != nil {
			return nil, fmt.Errorf("resolve proto file %q: %w", path, err)
		}
		dir := filepath.Dir(absolute)
		if !containsPath(importPaths, dir) {
			importPaths = append(importPaths, dir)
		}
		root := filepath.Base(absolute)
		for _, importPath := range importPaths {
			if relative, relErr := filepath.Rel(importPath, absolute); relErr == nil && relative != "." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
				root = filepath.ToSlash(relative)
				break
			}
		}
		roots = append(roots, root)
	}
	compiler := protocompile.Compiler{
		Resolver: protocompile.WithStandardImports(&protocompile.SourceResolver{ImportPaths: importPaths}),
	}
	compiled, err := compiler.Compile(context.Background(), roots...)
	if err != nil {
		return nil, fmt.Errorf("compile Protobuf schemas: %w", err)
	}
	out := make([]protoreflect.FileDescriptor, 0, len(compiled))
	for _, file := range compiled {
		out = append(out, file)
	}
	return out, nil
}

func containsPath(paths []string, candidate string) bool {
	for _, path := range paths {
		absolute, err := filepath.Abs(path)
		if err == nil && absolute == candidate {
			return true
		}
	}
	return false
}

func mergeFiles(destination, source *protoregistry.Files) error {
	var first error
	source.RangeFiles(func(file protoreflect.FileDescriptor) bool {
		if err := registerFileRecursive(destination, file, make(map[string]bool)); err != nil {
			first = err
			return false
		}
		return true
	})
	return first
}

func registerFileRecursive(files *protoregistry.Files, file protoreflect.FileDescriptor, visited map[string]bool) error {
	if visited[file.Path()] {
		return nil
	}
	visited[file.Path()] = true
	imports := file.Imports()
	for i := 0; i < imports.Len(); i++ {
		imported := imports.Get(i)
		if imported.FileDescriptor != nil {
			if err := registerFileRecursive(files, imported.FileDescriptor, visited); err != nil {
				return err
			}
		}
	}
	if _, err := files.FindFileByPath(file.Path()); err == nil {
		return nil
	}
	return files.RegisterFile(file)
}

func indexFile(registry *Registry, file protoreflect.FileDescriptor) {
	indexMessages(registry.messages, file.Messages())
	services := file.Services()
	for i := 0; i < services.Len(); i++ {
		service := services.Get(i)
		methods := service.Methods()
		for j := 0; j < methods.Len(); j++ {
			method := methods.Get(j)
			path := "/" + string(service.FullName()) + "/" + string(method.Name())
			registry.methods[path] = method
		}
	}
}

func indexMessages(index map[protoreflect.FullName]protoreflect.MessageDescriptor, messages protoreflect.MessageDescriptors) {
	for i := 0; i < messages.Len(); i++ {
		message := messages.Get(i)
		index[message.FullName()] = message
		indexMessages(index, message.Messages())
	}
}

func (r *Registry) Methods() []Method {
	if r == nil {
		return nil
	}
	out := make([]Method, 0, len(r.methods))
	for path, descriptor := range r.methods {
		out = append(out, Method{
			Path:            path,
			InputType:       string(descriptor.Input().FullName()),
			OutputType:      string(descriptor.Output().FullName()),
			ClientStreaming: descriptor.IsStreamingClient(),
			ServerStreaming: descriptor.IsStreamingServer(),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

func (r *Registry) Method(path string) (protoreflect.MethodDescriptor, bool) {
	if r == nil {
		return nil, false
	}
	method, ok := r.methods[path]
	return method, ok
}

func (r *Registry) DecodeRequest(path string, framed []byte) (any, error) {
	method, ok := r.Method(path)
	if !ok {
		return nil, fmt.Errorf("gRPC method %q is not present in loaded descriptors", path)
	}
	return decodeFramed(method.Input(), framed)
}

func (r *Registry) DecodeResponse(path string, framed []byte) (any, error) {
	method, ok := r.Method(path)
	if !ok {
		return nil, fmt.Errorf("gRPC method %q is not present in loaded descriptors", path)
	}
	return decodeFramed(method.Output(), framed)
}

func decodeFramed(descriptor protoreflect.MessageDescriptor, framed []byte) (any, error) {
	frames, err := splitFrames(framed)
	if err != nil {
		return nil, err
	}
	values := make([]any, 0, len(frames))
	for index, frame := range frames {
		message := dynamicpb.NewMessage(descriptor)
		if err := proto.Unmarshal(frame, message); err != nil {
			return nil, fmt.Errorf("decode Protobuf frame %d as %s: %w", index, descriptor.FullName(), err)
		}
		encoded, err := (protojson.MarshalOptions{UseProtoNames: true}).Marshal(message)
		if err != nil {
			return nil, fmt.Errorf("render Protobuf frame %d: %w", index, err)
		}
		var value any
		if err := json.Unmarshal(encoded, &value); err != nil {
			return nil, fmt.Errorf("normalize Protobuf frame %d: %w", index, err)
		}
		values = append(values, value)
	}
	if len(values) == 1 {
		return values[0], nil
	}
	return values, nil
}

func splitFrames(data []byte) ([][]byte, error) {
	var frames [][]byte
	for len(data) > 0 {
		if len(frames) >= maxStreamFrames {
			return nil, fmt.Errorf("gRPC stream exceeds %d messages", maxStreamFrames)
		}
		if len(data) < 5 {
			return nil, fmt.Errorf("truncated gRPC frame header")
		}
		if data[0] != 0 {
			return nil, fmt.Errorf("compressed gRPC frames are not supported for semantic decoding")
		}
		length := int(binary.BigEndian.Uint32(data[1:5]))
		if length > maxFrameBytes {
			return nil, fmt.Errorf("gRPC message exceeds %d-byte semantic limit", maxFrameBytes)
		}
		if length < 0 || len(data)-5 < length {
			return nil, fmt.Errorf("truncated gRPC frame payload")
		}
		frames = append(frames, append([]byte(nil), data[5:5+length]...))
		data = data[5+length:]
	}
	if len(frames) == 0 {
		return nil, fmt.Errorf("gRPC request contains no message frames")
	}
	return frames, nil
}

func (r *Registry) EncodeResponse(path string, documents []string) ([][]byte, error) {
	method, ok := r.Method(path)
	if !ok {
		return nil, fmt.Errorf("gRPC method %q is not present in loaded descriptors", path)
	}
	return encodeDocuments(method.Output(), documents)
}

func (r *Registry) EncodeMessage(typeName string, documents []string) ([][]byte, error) {
	if r == nil {
		return nil, fmt.Errorf("no Protobuf descriptors are loaded")
	}
	descriptor := r.messages[protoreflect.FullName(strings.TrimPrefix(typeName, "."))]
	if descriptor == nil {
		return nil, fmt.Errorf("Protobuf message %q is not present in loaded descriptors", typeName)
	}
	return encodeDocuments(descriptor, documents)
}

func encodeDocuments(descriptor protoreflect.MessageDescriptor, documents []string) ([][]byte, error) {
	if len(documents) == 0 {
		documents = []string{"{}"}
	}
	frames := make([][]byte, 0, len(documents))
	for index, document := range documents {
		message := dynamicpb.NewMessage(descriptor)
		if err := (protojson.UnmarshalOptions{DiscardUnknown: false}).Unmarshal([]byte(document), message); err != nil {
			return nil, fmt.Errorf("encode Protobuf response frame %d as %s: %w", index, descriptor.FullName(), err)
		}
		payload, err := proto.Marshal(message)
		if err != nil {
			return nil, fmt.Errorf("marshal Protobuf response frame %d: %w", index, err)
		}
		frame := make([]byte, 5+len(payload))
		binary.BigEndian.PutUint32(frame[1:5], uint32(len(payload)))
		copy(frame[5:], payload)
		frames = append(frames, frame)
	}
	return frames, nil
}

func (r *Registry) ResponseExample(path string) (string, error) {
	method, ok := r.Method(path)
	if !ok {
		return "", fmt.Errorf("gRPC method %q is not present in loaded descriptors", path)
	}
	value := exampleMessage(method.Output(), make(map[protoreflect.FullName]bool))
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func exampleMessage(message protoreflect.MessageDescriptor, resolving map[protoreflect.FullName]bool) map[string]any {
	if resolving[message.FullName()] {
		return map[string]any{}
	}
	resolving[message.FullName()] = true
	defer delete(resolving, message.FullName())
	out := make(map[string]any)
	fields := message.Fields()
	for i := 0; i < fields.Len(); i++ {
		field := fields.Get(i)
		if field.ContainingOneof() != nil && field.ContainingOneof().Fields().Get(0).Number() != field.Number() {
			continue
		}
		value := exampleField(field, resolving)
		if field.IsList() {
			value = []any{value}
		} else if field.IsMap() {
			value = map[string]any{"key": exampleField(field.MapValue(), resolving)}
		}
		out[field.JSONName()] = value
	}
	return out
}

func exampleField(field protoreflect.FieldDescriptor, resolving map[protoreflect.FullName]bool) any {
	switch field.Kind() {
	case protoreflect.BoolKind:
		return true
	case protoreflect.EnumKind:
		values := field.Enum().Values()
		if values.Len() > 0 {
			return string(values.Get(0).Name())
		}
		return ""
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind,
		protoreflect.Uint32Kind, protoreflect.Fixed32Kind,
		protoreflect.FloatKind, protoreflect.DoubleKind:
		return 1
	case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind,
		protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		return "1"
	case protoreflect.BytesKind:
		return ""
	case protoreflect.MessageKind, protoreflect.GroupKind:
		return exampleMessage(field.Message(), resolving)
	default:
		return "example"
	}
}
