package simtemplate

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"text/template"
	"time"
)

const (
	defaultMaxOutput = 1 << 20
	maxTemplateBytes = 256 << 10
)

type Config struct {
	Seed           string `yaml:"seed" json:"seed,omitempty"`
	MaxOutputBytes int    `yaml:"max_output_bytes" json:"max_output_bytes,omitempty"`
}

type Request struct {
	Method   string
	URL      string
	Path     string
	Headers  http.Header
	Query    url.Values
	JSON     any
	Protobuf any
	Body     string
}

type Data struct {
	Request Request
}

type Engine struct {
	seed      string
	maxOutput int
}

func New(cfg Config) (*Engine, error) {
	if cfg.MaxOutputBytes < 0 {
		return nil, fmt.Errorf("templates.max_output_bytes must be >= 0")
	}
	maxOutput := cfg.MaxOutputBytes
	if maxOutput == 0 {
		maxOutput = defaultMaxOutput
	}
	seed := cfg.Seed
	if seed == "" {
		seed = "infernosim"
	}
	return &Engine{seed: seed, maxOutput: maxOutput}, nil
}

func Validate(value string) error {
	if value == "" {
		return nil
	}
	if len(value) > maxTemplateBytes {
		return fmt.Errorf("template exceeds %d bytes", maxTemplateBytes)
	}
	_, err := template.New("validation").Option("missingkey=error").Funcs(validationFunctions()).Parse(value)
	return err
}

func validationFunctions() template.FuncMap {
	return template.FuncMap{
		"jsonPath": func(string) any { return "" },
		"proto":    func(string) any { return "" },
		"header":   func(string) string { return "" },
		"query":    func(string) string { return "" },
		"uuid":     func(string) string { return "" },
		"token":    func(string) string { return "" },
		"now":      func() string { return "" },
		"nowUnix":  func() int64 { return 0 },
		"toJSON":   func(any) (string, error) { return "", nil },
		"default":  defaultValue,
	}
}

func (e *Engine) Render(name, value string, data Data) (string, error) {
	if value == "" {
		return "", nil
	}
	if len(value) > maxTemplateBytes {
		return "", fmt.Errorf("%s template exceeds %d bytes", name, maxTemplateBytes)
	}
	fingerprint := requestFingerprint(e.seed, data)
	stableTime := deterministicTime(fingerprint)
	functions := template.FuncMap{
		"jsonPath": func(path string) any {
			value, _ := lookup(data.Request.JSON, path)
			return value
		},
		"proto": func(path string) any {
			value, _ := lookup(data.Request.Protobuf, path)
			return value
		},
		"header": func(name string) string {
			return data.Request.Headers.Get(name)
		},
		"query": func(name string) string {
			return data.Request.Query.Get(name)
		},
		"uuid": func(label string) string {
			sum := sha256.Sum256([]byte(fingerprint + "\x00uuid\x00" + label))
			raw := append([]byte(nil), sum[:16]...)
			raw[6] = (raw[6] & 0x0f) | 0x40
			raw[8] = (raw[8] & 0x3f) | 0x80
			encoded := hex.EncodeToString(raw)
			return encoded[0:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:32]
		},
		"token": func(label string) string {
			sum := sha256.Sum256([]byte(fingerprint + "\x00token\x00" + label))
			return hex.EncodeToString(sum[:16])
		},
		"now": func() string {
			return stableTime.Format(time.RFC3339Nano)
		},
		"nowUnix": func() int64 {
			return stableTime.Unix()
		},
		"toJSON": func(value any) (string, error) {
			encoded, err := json.Marshal(value)
			return string(encoded), err
		},
		"default": defaultValue,
	}
	parsed, err := template.New(name).Option("missingkey=error").Funcs(functions).Parse(value)
	if err != nil {
		return "", fmt.Errorf("%s template: %w", name, err)
	}
	var output bytes.Buffer
	if err := parsed.Execute(&output, data); err != nil {
		return "", fmt.Errorf("%s template: %w", name, err)
	}
	if output.Len() > e.maxOutput {
		return "", fmt.Errorf("%s template output exceeds %d bytes", name, e.maxOutput)
	}
	return output.String(), nil
}

func (e *Engine) RenderHeader(prefix string, values http.Header, data Data) (http.Header, error) {
	if values == nil {
		return make(http.Header), nil
	}
	out := make(http.Header, len(values))
	for name, entries := range values {
		for index, entry := range entries {
			rendered, err := e.Render(fmt.Sprintf("%s.%s[%d]", prefix, name, index), entry, data)
			if err != nil {
				return nil, err
			}
			if strings.ContainsAny(rendered, "\r\n") {
				return nil, fmt.Errorf("%s.%s[%d] contains a forbidden newline", prefix, name, index)
			}
			out.Add(name, rendered)
		}
	}
	return out, nil
}

func defaultValue(fallback, value any) any {
	if value == nil {
		return fallback
	}
	switch typed := value.(type) {
	case string:
		if typed == "" {
			return fallback
		}
	case []any:
		if len(typed) == 0 {
			return fallback
		}
	case map[string]any:
		if len(typed) == 0 {
			return fallback
		}
	}
	return value
}

func requestFingerprint(seed string, data Data) string {
	hash := sha256.New()
	for _, value := range []string{
		seed,
		data.Request.Method,
		data.Request.URL,
		data.Request.Path,
		data.Request.Query.Encode(),
		data.Request.Body,
	} {
		_, _ = hash.Write([]byte(strconv.Itoa(len(value))))
		_, _ = hash.Write([]byte{':'})
		_, _ = hash.Write([]byte(value))
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func deterministicTime(fingerprint string) time.Time {
	decoded, _ := hex.DecodeString(fingerprint[:16])
	const tenYears = int64(10 * 365 * 24 * 60 * 60)
	seconds := int64(binary.BigEndian.Uint64(decoded) % uint64(tenYears))
	return time.Unix(1704067200+seconds, 0).UTC()
}

func lookup(root any, path string) (any, bool) {
	if path == "" || path == "$" {
		return root, root != nil
	}
	if !strings.HasPrefix(path, "$") {
		path = "$." + path
	}
	current := root
	remaining := strings.TrimPrefix(path, "$")
	for remaining != "" {
		switch remaining[0] {
		case '.':
			remaining = remaining[1:]
			end := strings.IndexAny(remaining, ".[")
			if end < 0 {
				end = len(remaining)
			}
			key := remaining[:end]
			object, ok := current.(map[string]any)
			if !ok {
				return nil, false
			}
			current, ok = object[key]
			if !ok {
				return nil, false
			}
			remaining = remaining[end:]
		case '[':
			end := strings.IndexByte(remaining, ']')
			if end < 0 {
				return nil, false
			}
			index, err := strconv.Atoi(remaining[1:end])
			if err != nil {
				return nil, false
			}
			array, ok := current.([]any)
			if !ok || index < 0 || index >= len(array) {
				return nil, false
			}
			current = array[index]
			remaining = remaining[end+1:]
		default:
			return nil, false
		}
	}
	return current, true
}
