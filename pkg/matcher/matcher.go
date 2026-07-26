package matcher

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"infernosim/pkg/event"
)

// Config controls semantic matching of outbound requests against captured
// dependency calls. Empty configuration preserves the legacy exact
// method/host/path/query behavior.
type Config struct {
	IgnoredQueryParameters []string `yaml:"ignored_query_parameters" json:"ignored_query_parameters,omitempty"`
	IgnoredHeaders         []string `yaml:"ignored_headers" json:"ignored_headers,omitempty"`
	IgnoredJSONPaths       []string `yaml:"ignored_json_paths" json:"ignored_json_paths,omitempty"`
	Rules                  []Rule   `yaml:"rules" json:"rules,omitempty"`
}

// Rule applies semantic constraints to requests whose method and path match.
// Regex values use Go's RE2 syntax. JSONPath supports $, dotted object keys,
// and numeric array indexes, for example $.orders[0].id.
type Rule struct {
	Name                   string            `yaml:"name" json:"name,omitempty"`
	Methods                []string          `yaml:"methods" json:"methods,omitempty"`
	HostRegex              string            `yaml:"host_regex" json:"host_regex,omitempty"`
	PathRegex              string            `yaml:"path_regex" json:"path_regex,omitempty"`
	HeaderRegex            map[string]string `yaml:"header_regex" json:"header_regex,omitempty"`
	QueryRegex             map[string]string `yaml:"query_regex" json:"query_regex,omitempty"`
	JSONPathRegex          map[string]string `yaml:"jsonpath_regex" json:"jsonpath_regex,omitempty"`
	IgnoredQueryParameters []string          `yaml:"ignored_query_parameters" json:"ignored_query_parameters,omitempty"`
	IgnoredHeaders         []string          `yaml:"ignored_headers" json:"ignored_headers,omitempty"`
	IgnoredJSONPaths       []string          `yaml:"ignored_json_paths" json:"ignored_json_paths,omitempty"`
	CompareHeaders         bool              `yaml:"compare_headers" json:"compare_headers,omitempty"`
	CompareJSON            bool              `yaml:"compare_json" json:"compare_json,omitempty"`
}

type compiledRule struct {
	rule       Rule
	host       *regexp.Regexp
	path       *regexp.Regexp
	headers    map[string]*regexp.Regexp
	query      map[string]*regexp.Regexp
	jsonValues map[string]*regexp.Regexp
}

// Matcher is immutable after construction and safe for concurrent use.
type Matcher struct {
	cfg   Config
	rules []compiledRule
}

func New(cfg Config) (*Matcher, error) {
	m := &Matcher{cfg: cfg}
	for i, rule := range cfg.Rules {
		cr := compiledRule{
			rule:       rule,
			headers:    make(map[string]*regexp.Regexp),
			query:      make(map[string]*regexp.Regexp),
			jsonValues: make(map[string]*regexp.Regexp),
		}
		var err error
		if rule.HostRegex != "" {
			if cr.host, err = regexp.Compile(rule.HostRegex); err != nil {
				return nil, fmt.Errorf("matching.rules[%d].host_regex: %w", i, err)
			}
		}
		if rule.PathRegex != "" {
			if cr.path, err = regexp.Compile(rule.PathRegex); err != nil {
				return nil, fmt.Errorf("matching.rules[%d].path_regex: %w", i, err)
			}
		}
		for name, pattern := range rule.HeaderRegex {
			re, compileErr := regexp.Compile(pattern)
			if compileErr != nil {
				return nil, fmt.Errorf("matching.rules[%d].header_regex[%s]: %w", i, name, compileErr)
			}
			cr.headers[http.CanonicalHeaderKey(name)] = re
		}
		for name, pattern := range rule.QueryRegex {
			re, compileErr := regexp.Compile(pattern)
			if compileErr != nil {
				return nil, fmt.Errorf("matching.rules[%d].query_regex[%s]: %w", i, name, compileErr)
			}
			cr.query[name] = re
		}
		for path, pattern := range rule.JSONPathRegex {
			if _, pathErr := parseJSONPath(path); pathErr != nil {
				return nil, fmt.Errorf("matching.rules[%d].jsonpath_regex[%s]: %w", i, path, pathErr)
			}
			re, compileErr := regexp.Compile(pattern)
			if compileErr != nil {
				return nil, fmt.Errorf("matching.rules[%d].jsonpath_regex[%s]: %w", i, path, compileErr)
			}
			cr.jsonValues[path] = re
		}
		for _, path := range append(append([]string{}, cfg.IgnoredJSONPaths...), rule.IgnoredJSONPaths...) {
			if _, err := parseJSONPath(path); err != nil {
				return nil, fmt.Errorf("matching ignored JSONPath %q: %w", path, err)
			}
		}
		m.rules = append(m.rules, cr)
	}
	return m, nil
}

// Match compares one incoming request with a captured outbound event.
func (m *Matcher) Match(captured event.Event, req *http.Request, body []byte) (bool, string) {
	if req == nil || req.URL == nil {
		return false, "missing request URL"
	}
	capturedURL, err := url.Parse(captured.URL)
	if err != nil {
		return false, "captured URL is invalid"
	}
	reqHost := req.URL.Host
	if reqHost == "" {
		reqHost = req.Host
	}
	if !strings.EqualFold(strings.ToUpper(req.Method), strings.ToUpper(captured.Method)) {
		return false, "method mismatch"
	}

	cr := m.ruleFor(req.Method, reqHost, escapedPath(req.URL))
	if cr == nil {
		if !equalHost(reqHost, capturedURL.Host) {
			return false, "host mismatch"
		}
		if escapedPath(req.URL) != escapedPath(capturedURL) {
			return false, "path mismatch"
		}
		if !equalQuery(req.URL.Query(), capturedURL.Query(), m.cfg.IgnoredQueryParameters) {
			return false, "query mismatch"
		}
		return true, ""
	}

	if !methodAllowed(cr.rule.Methods, captured.Method) {
		return false, "captured method does not satisfy rule"
	}
	if cr.host != nil && (!cr.host.MatchString(stripPort(reqHost)) || !cr.host.MatchString(stripPort(capturedURL.Host))) {
		return false, "host regex mismatch"
	}
	if cr.host == nil && !equalHost(reqHost, capturedURL.Host) {
		return false, "host mismatch"
	}
	if cr.path != nil {
		if !cr.path.MatchString(req.URL.Path) || !cr.path.MatchString(capturedURL.Path) {
			return false, "path regex mismatch"
		}
	} else if escapedPath(req.URL) != escapedPath(capturedURL) {
		return false, "path mismatch"
	}

	ignoredQuery := append(append([]string{}, m.cfg.IgnoredQueryParameters...), cr.rule.IgnoredQueryParameters...)
	for name := range cr.query {
		ignoredQuery = append(ignoredQuery, name)
	}
	if !equalQuery(req.URL.Query(), capturedURL.Query(), ignoredQuery) {
		return false, "query mismatch"
	}
	for name, re := range cr.query {
		if !re.MatchString(req.URL.Query().Get(name)) {
			return false, "query regex mismatch: " + name
		}
	}
	for name, re := range cr.headers {
		if !re.MatchString(req.Header.Get(name)) {
			return false, "header regex mismatch: " + name
		}
	}
	if cr.rule.CompareHeaders {
		ignoredHeaders := append(append([]string{}, m.cfg.IgnoredHeaders...), cr.rule.IgnoredHeaders...)
		for name := range cr.headers {
			ignoredHeaders = append(ignoredHeaders, name)
		}
		if !equalHeaders(req.Header, http.Header(captured.Headers), ignoredHeaders) {
			return false, "header mismatch"
		}
	}

	var requestJSON any
	if len(cr.jsonValues) > 0 || cr.rule.CompareJSON {
		if err := json.Unmarshal(body, &requestJSON); err != nil {
			return false, "request body is not valid JSON"
		}
	}
	for path, re := range cr.jsonValues {
		value, ok := JSONPathValue(requestJSON, path)
		if !ok || !re.MatchString(stringValue(value)) {
			return false, "JSONPath regex mismatch: " + path
		}
	}
	if cr.rule.CompareJSON {
		capturedBody, err := base64.StdEncoding.DecodeString(captured.BodyB64)
		if err != nil || len(capturedBody) == 0 {
			return false, "captured JSON body unavailable"
		}
		var expectedJSON any
		if err := json.Unmarshal(capturedBody, &expectedJSON); err != nil {
			return false, "captured body is not valid JSON"
		}
		ignoredPaths := append(append([]string{}, m.cfg.IgnoredJSONPaths...), cr.rule.IgnoredJSONPaths...)
		for path := range cr.jsonValues {
			ignoredPaths = append(ignoredPaths, path)
		}
		for _, path := range ignoredPaths {
			removeJSONPath(requestJSON, path)
			removeJSONPath(expectedJSON, path)
		}
		actualCanonical, _ := json.Marshal(requestJSON)
		expectedCanonical, _ := json.Marshal(expectedJSON)
		if !bytes.Equal(actualCanonical, expectedCanonical) {
			return false, "JSON body mismatch"
		}
	}
	return true, ""
}

// MatchRule evaluates a standalone rule. Stateful scenarios use this without a
// corresponding captured event.
func MatchRule(rule Rule, req *http.Request, body []byte) (bool, string, error) {
	cfg := Config{Rules: []Rule{rule}}
	m, err := New(cfg)
	if err != nil {
		return false, "", err
	}
	host := req.Host
	if req.URL != nil && req.URL.Host != "" {
		host = req.URL.Host
	}
	cr := m.ruleFor(req.Method, host, req.URL.Path)
	if cr == nil {
		return false, "rule selector mismatch", nil
	}
	for name, re := range cr.headers {
		if !re.MatchString(req.Header.Get(name)) {
			return false, "header regex mismatch: " + name, nil
		}
	}
	for name, re := range cr.query {
		if !re.MatchString(req.URL.Query().Get(name)) {
			return false, "query regex mismatch: " + name, nil
		}
	}
	if len(cr.jsonValues) > 0 {
		var value any
		if err := json.Unmarshal(body, &value); err != nil {
			return false, "request body is not valid JSON", nil
		}
		for path, re := range cr.jsonValues {
			got, ok := JSONPathValue(value, path)
			if !ok || !re.MatchString(stringValue(got)) {
				return false, "JSONPath regex mismatch: " + path, nil
			}
		}
	}
	return true, "", nil
}

func (m *Matcher) ruleFor(method, host, path string) *compiledRule {
	for i := range m.rules {
		cr := &m.rules[i]
		if !methodAllowed(cr.rule.Methods, method) {
			continue
		}
		if cr.host != nil && !cr.host.MatchString(stripPort(host)) {
			continue
		}
		if cr.path != nil && !cr.path.MatchString(path) {
			continue
		}
		return cr
	}
	return nil
}

func methodAllowed(methods []string, method string) bool {
	if len(methods) == 0 {
		return true
	}
	for _, candidate := range methods {
		if strings.EqualFold(candidate, method) {
			return true
		}
	}
	return false
}

func equalHost(a, b string) bool {
	return strings.EqualFold(stripDefaultPort(a), stripDefaultPort(b))
}

func stripDefaultPort(host string) string {
	lower := strings.ToLower(host)
	return strings.TrimSuffix(strings.TrimSuffix(lower, ":80"), ":443")
}

func stripPort(host string) string {
	if parsed, _, err := net.SplitHostPort(host); err == nil {
		return strings.Trim(parsed, "[]")
	}
	return strings.Trim(host, "[]")
}

func escapedPath(u *url.URL) string {
	if u == nil || u.EscapedPath() == "" {
		return "/"
	}
	return u.EscapedPath()
}

func equalQuery(a, b url.Values, ignored []string) bool {
	ignore := make(map[string]struct{}, len(ignored))
	for _, name := range ignored {
		ignore[name] = struct{}{}
	}
	clone := func(values url.Values) url.Values {
		out := make(url.Values)
		for key, vals := range values {
			if _, skip := ignore[key]; skip {
				continue
			}
			out[key] = append([]string(nil), vals...)
		}
		return out
	}
	return clone(a).Encode() == clone(b).Encode()
}

func equalHeaders(a, b http.Header, ignored []string) bool {
	ignore := make(map[string]struct{}, len(ignored))
	for _, name := range ignored {
		ignore[http.CanonicalHeaderKey(name)] = struct{}{}
	}
	canonical := func(headers http.Header) map[string]string {
		out := make(map[string]string)
		for name, values := range headers {
			name = http.CanonicalHeaderKey(name)
			if _, skip := ignore[name]; skip {
				continue
			}
			copied := append([]string(nil), values...)
			sort.Strings(copied)
			out[name] = strings.Join(copied, "\x00")
		}
		return out
	}
	left := canonical(a)
	right := canonical(b)
	if len(left) != len(right) {
		return false
	}
	for name, value := range left {
		if right[name] != value {
			return false
		}
	}
	return true
}

type jsonPathToken struct {
	key   string
	index int
	array bool
}

func parseJSONPath(path string) ([]jsonPathToken, error) {
	if path == "$" {
		return nil, nil
	}
	if !strings.HasPrefix(path, "$.") {
		return nil, fmt.Errorf("must begin with $ or $.")
	}
	remaining := path[2:]
	var tokens []jsonPathToken
	for remaining != "" {
		end := strings.IndexAny(remaining, ".[")
		if end == -1 {
			end = len(remaining)
		}
		if end > 0 {
			tokens = append(tokens, jsonPathToken{key: remaining[:end]})
			remaining = remaining[end:]
		}
		if strings.HasPrefix(remaining, ".") {
			remaining = remaining[1:]
			continue
		}
		if strings.HasPrefix(remaining, "[") {
			closeAt := strings.IndexByte(remaining, ']')
			if closeAt < 2 {
				return nil, fmt.Errorf("invalid array index")
			}
			index, err := strconv.Atoi(remaining[1:closeAt])
			if err != nil || index < 0 {
				return nil, fmt.Errorf("invalid array index")
			}
			tokens = append(tokens, jsonPathToken{index: index, array: true})
			remaining = remaining[closeAt+1:]
			if strings.HasPrefix(remaining, ".") {
				remaining = remaining[1:]
			}
			continue
		}
		if end == 0 && remaining != "" {
			return nil, fmt.Errorf("invalid JSONPath near %q", remaining)
		}
	}
	return tokens, nil
}

// JSONPathValue extracts a value using InfernoSIM's deliberately small,
// deterministic JSONPath subset.
func JSONPathValue(root any, path string) (any, bool) {
	tokens, err := parseJSONPath(path)
	if err != nil {
		return nil, false
	}
	current := root
	for _, token := range tokens {
		if token.array {
			array, ok := current.([]any)
			if !ok || token.index >= len(array) {
				return nil, false
			}
			current = array[token.index]
			continue
		}
		object, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		current, ok = object[token.key]
		if !ok {
			return nil, false
		}
	}
	return current, true
}

func removeJSONPath(root any, path string) {
	tokens, err := parseJSONPath(path)
	if err != nil || len(tokens) == 0 {
		return
	}
	current := root
	for _, token := range tokens[:len(tokens)-1] {
		if token.array {
			array, ok := current.([]any)
			if !ok || token.index >= len(array) {
				return
			}
			current = array[token.index]
		} else {
			object, ok := current.(map[string]any)
			if !ok {
				return
			}
			current = object[token.key]
		}
	}
	last := tokens[len(tokens)-1]
	if last.array {
		array, ok := current.([]any)
		if ok && last.index < len(array) {
			array[last.index] = nil
		}
		return
	}
	if object, ok := current.(map[string]any); ok {
		delete(object, last.key)
	}
}

func stringValue(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case nil:
		return ""
	default:
		encoded, _ := json.Marshal(typed)
		return string(encoded)
	}
}
