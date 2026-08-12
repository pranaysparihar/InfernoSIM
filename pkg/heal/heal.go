// Package heal turns repeated captured dependency calls into narrow,
// explainable semantic matcher proposals. It is deliberately conservative:
// protected business/security fields are never relaxed and ambiguous matcher
// configurations are rejected before a proposed replay file is written.
package heal

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"infernosim/pkg/event"
	"infernosim/pkg/matcher"
	"infernosim/pkg/replaydriver"
	"infernosim/pkg/stubproxy"

	"gopkg.in/yaml.v3"
)

const reportVersion = 1

type Options struct {
	IncidentDirs   []string
	ConfigPath     string
	OutputPath     string
	ReportDir      string
	MinimumSamples int
	MinimumScore   float64
	Apply          bool
	Force          bool
}

type Proposal struct {
	Rule       string  `json:"rule"`
	Kind       string  `json:"kind"`
	Location   string  `json:"location"`
	Pattern    string  `json:"pattern,omitempty"`
	Samples    int     `json:"samples"`
	Confidence float64 `json:"confidence"`
	Decision   string  `json:"decision"`
	Reason     string  `json:"reason"`
	Evidence   string  `json:"evidence_sha256"`
}

type Result struct {
	Version          int        `json:"version"`
	Generated        time.Time  `json:"generated"`
	InputHash        string     `json:"input_hash"`
	ProposedHash     string     `json:"proposed_config_hash,omitempty"`
	Accepted         int        `json:"accepted"`
	Rejected         int        `json:"rejected"`
	GroupsAnalyzed   int        `json:"groups_analyzed"`
	Ambiguities      int        `json:"ambiguities"`
	OutputPath       string     `json:"output_path,omitempty"`
	AppliedPath      string     `json:"applied_path,omitempty"`
	BackupPath       string     `json:"backup_path,omitempty"`
	Proposals        []Proposal `json:"proposals"`
	ValidationPassed bool       `json:"validation_passed"`
}

type observation struct {
	event event.Event
	body  []byte
	json  map[string]string
}

type group struct {
	key       string
	name      string
	method    string
	host      string
	pathRegex string
	items     []observation
}

var (
	uuidPattern       = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	rfc3339Pattern    = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})$`)
	hexPattern        = regexp.MustCompile(`(?i)^[0-9a-f]{16,128}$`)
	base64URLPattern  = regexp.MustCompile(`^[A-Za-z0-9_-]{20,}={0,2}$`)
	integerPattern    = regexp.MustCompile(`^-?[0-9]{10,19}$`)
	protectedNamePart = regexp.MustCompile(`(?i)(^|[_.-])(authorization|password|passwd|secret|api[_-]?key|tenant|account|permission|role|scope|amount|price|currency|status|email|phone|card|ssn)([_.-]|$)`)
	volatileNamePart  = regexp.MustCompile(`(?i)(^|[_.-])(request[_-]?id|trace[_-]?id|correlation[_-]?id|nonce|timestamp|created[_-]?at|updated[_-]?at|expires[_-]?at|time)([_.-]|$)`)
)

func Run(opts Options) (Result, error) {
	if len(opts.IncidentDirs) == 0 {
		return Result{}, fmt.Errorf("at least one incident directory is required")
	}
	if opts.MinimumSamples == 0 {
		opts.MinimumSamples = 3
	}
	if opts.MinimumSamples < 2 {
		return Result{}, fmt.Errorf("minimum samples must be at least 2")
	}
	if opts.MinimumScore == 0 {
		opts.MinimumScore = 0.95
	}
	if math.IsNaN(opts.MinimumScore) || math.IsInf(opts.MinimumScore, 0) || opts.MinimumScore < 0 || opts.MinimumScore > 1 {
		return Result{}, fmt.Errorf("minimum confidence must be between 0 and 1")
	}
	if opts.OutputPath == "" {
		opts.OutputPath = filepath.Join(opts.IncidentDirs[0], "replay.proposed.yaml")
	}
	if opts.ReportDir == "" {
		opts.ReportDir = filepath.Join(opts.IncidentDirs[0], "reports")
	}
	if !opts.Force {
		if _, err := os.Stat(opts.OutputPath); err == nil {
			return Result{}, fmt.Errorf("refusing to overwrite %s without force", opts.OutputPath)
		} else if !os.IsNotExist(err) {
			return Result{}, err
		}
	}

	var config replaydriver.ReplayYAMLConfig
	configPath := opts.ConfigPath
	if configPath == "" {
		candidate := filepath.Join(opts.IncidentDirs[0], "replay.yaml")
		if _, err := os.Stat(candidate); err == nil {
			configPath = candidate
		}
	}
	activePath := configPath
	if activePath == "" {
		activePath = filepath.Join(opts.IncidentDirs[0], "replay.yaml")
	}
	if filepath.Clean(opts.OutputPath) == filepath.Clean(activePath) {
		return Result{}, fmt.Errorf("proposed output must differ from the active replay configuration; use --apply for promotion")
	}
	if configPath != "" {
		loaded, err := replaydriver.LoadReplayConfig(configPath)
		if err != nil {
			return Result{}, err
		}
		config = loaded
	}

	groups, allEvents, inputHash, err := loadGroups(opts.IncidentDirs)
	if err != nil {
		return Result{}, err
	}
	result := Result{Version: reportVersion, Generated: time.Now().UTC(), InputHash: inputHash, GroupsAnalyzed: len(groups)}
	generatedRules := make([]matcher.Rule, 0)
	for _, grouped := range groups {
		rule, proposals := analyzeGroup(grouped, opts.MinimumSamples, opts.MinimumScore)
		result.Proposals = append(result.Proposals, proposals...)
		if len(rule.HeaderRegex)+len(rule.QueryRegex)+len(rule.JSONPathRegex) > 0 {
			generatedRules = append(generatedRules, rule)
		}
	}
	sort.Slice(result.Proposals, func(i, j int) bool {
		if result.Proposals[i].Rule == result.Proposals[j].Rule {
			if result.Proposals[i].Kind == result.Proposals[j].Kind {
				return result.Proposals[i].Location < result.Proposals[j].Location
			}
			return result.Proposals[i].Kind < result.Proposals[j].Kind
		}
		return result.Proposals[i].Rule < result.Proposals[j].Rule
	})
	for _, proposal := range result.Proposals {
		if proposal.Decision == "accepted" {
			result.Accepted++
		} else {
			result.Rejected++
		}
	}
	config.Matching.Rules = mergeGeneratedRules(config.Matching.Rules, generatedRules)
	ambiguities, err := ambiguityCount(config.Matching, allEvents)
	if err != nil {
		return Result{}, err
	}
	result.Ambiguities = ambiguities
	result.ValidationPassed = ambiguities == 0
	if ambiguities > 0 {
		if reportErr := writeReports(opts.ReportDir, result); reportErr != nil {
			return result, fmt.Errorf("healing introduced %d ambiguous matches; report also failed: %v", ambiguities, reportErr)
		}
		return result, fmt.Errorf("healing introduced %d ambiguous matches; no configuration was written", ambiguities)
	}
	data, err := yaml.Marshal(config)
	if err != nil {
		return result, err
	}
	header := []byte("# Proposed by InfernoSIM heal. Review before replacing replay.yaml.\n")
	data = append(header, data...)
	result.ProposedHash = hashBytes(data)
	result.OutputPath = opts.OutputPath
	if err := writeAtomic(opts.OutputPath, data); err != nil {
		return result, err
	}
	if opts.Apply {
		applyPath := configPath
		if applyPath == "" {
			applyPath = filepath.Join(opts.IncidentDirs[0], "replay.yaml")
		}
		if existing, readErr := os.ReadFile(applyPath); readErr == nil {
			backupPath, err := nextBackupPath(applyPath)
			if err != nil {
				return result, err
			}
			if err := writeAtomic(backupPath, existing); err != nil {
				return result, fmt.Errorf("back up active replay configuration: %w", err)
			}
			result.BackupPath = backupPath
		} else if !os.IsNotExist(readErr) {
			return result, readErr
		}
		if err := writeAtomic(applyPath, data); err != nil {
			return result, fmt.Errorf("apply replay configuration: %w", err)
		}
		result.AppliedPath = applyPath
	}
	if err := writeReports(opts.ReportDir, result); err != nil {
		return result, err
	}
	return result, nil
}

func nextBackupPath(activePath string) (string, error) {
	for index := 0; index < 1000; index++ {
		candidate := activePath + ".bak"
		if index > 0 {
			candidate += "." + strconv.Itoa(index)
		}
		_, err := os.Stat(candidate)
		if os.IsNotExist(err) {
			return candidate, nil
		}
		if err != nil {
			return "", err
		}
	}
	return "", fmt.Errorf("too many replay configuration backups for %s", activePath)
}

func loadGroups(incidentDirs []string) ([]group, []event.Event, string, error) {
	grouped := make(map[string]*group)
	var all []event.Event
	hash := sha256.New()
	for _, incidentDir := range incidentDirs {
		bundle, err := replaydriver.OpenBundle(incidentDir)
		if err != nil {
			return nil, nil, "", err
		}
		data, err := os.ReadFile(bundle.OutboundLog)
		if err != nil {
			return nil, nil, "", fmt.Errorf("heal requires outbound.log in %s: %w", incidentDir, err)
		}
		_, _ = hash.Write(data)
		events, err := stubproxy.LoadOutboundEvents(bundle.OutboundLog)
		if err != nil {
			return nil, nil, "", err
		}
		for _, captured := range events {
			parsed, err := url.Parse(captured.URL)
			if err != nil {
				continue
			}
			pathRegex := normalizedPathRegex(parsed.Path)
			key := strings.ToUpper(captured.Method) + "\x00" + strings.ToLower(parsed.Host) + "\x00" + pathRegex
			entry := grouped[key]
			if entry == nil {
				entry = &group{
					key:       key,
					name:      generatedRuleName(captured.Method, parsed.Host, parsed.Path),
					method:    strings.ToUpper(captured.Method),
					host:      parsed.Hostname(),
					pathRegex: pathRegex,
				}
				grouped[key] = entry
			}
			body, _ := base64.StdEncoding.DecodeString(captured.BodyB64)
			entry.items = append(entry.items, observation{event: captured, body: body, json: flattenJSON(body)})
			all = append(all, captured)
		}
	}
	keys := make([]string, 0, len(grouped))
	for key := range grouped {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	groups := make([]group, 0, len(keys))
	for _, key := range keys {
		groups = append(groups, *grouped[key])
	}
	return groups, all, hex.EncodeToString(hash.Sum(nil)), nil
}

func analyzeGroup(grouped group, minimumSamples int, minimumScore float64) (matcher.Rule, []Proposal) {
	rule := matcher.Rule{
		Name:          grouped.name,
		Methods:       []string{grouped.method},
		HostRegex:     "^" + regexp.QuoteMeta(grouped.host) + "$",
		PathRegex:     grouped.pathRegex,
		HeaderRegex:   map[string]string{},
		QueryRegex:    map[string]string{},
		JSONPathRegex: map[string]string{},
	}
	if len(grouped.items) < minimumSamples {
		return rule, nil
	}
	var proposals []Proposal
	add := func(kind, location string, values []string) {
		if !varies(values) {
			return
		}
		proposal := classify(grouped.name, kind, location, values)
		if proposal.Confidence < minimumScore && proposal.Decision == "accepted" {
			proposal.Decision = "rejected"
			proposal.Reason = fmt.Sprintf("confidence %.2f is below threshold %.2f", proposal.Confidence, minimumScore)
		}
		if proposal.Decision == "accepted" {
			switch kind {
			case "header_regex":
				rule.HeaderRegex[location] = proposal.Pattern
			case "query_regex":
				rule.QueryRegex[location] = proposal.Pattern
			case "jsonpath_regex":
				rule.JSONPathRegex[location] = proposal.Pattern
				rule.CompareJSON = true
			}
		}
		proposals = append(proposals, proposal)
	}

	for _, name := range commonHeaderNames(grouped.items) {
		values := make([]string, len(grouped.items))
		for index, item := range grouped.items {
			values[index] = headerValue(item.event.Headers, name)
		}
		add("header_regex", name, values)
	}
	for _, name := range commonQueryNames(grouped.items) {
		values := make([]string, len(grouped.items))
		for index, item := range grouped.items {
			parsed, _ := url.Parse(item.event.URL)
			values[index] = parsed.Query().Get(name)
		}
		add("query_regex", name, values)
	}
	for _, path := range commonJSONPaths(grouped.items) {
		values := make([]string, len(grouped.items))
		for index, item := range grouped.items {
			values[index] = item.json[path]
		}
		add("jsonpath_regex", path, values)
	}
	return rule, proposals
}

func classify(rule, kind, location string, values []string) Proposal {
	proposal := Proposal{
		Rule: rule, Kind: kind, Location: location, Samples: len(values),
		Decision: "rejected", Evidence: hashValues(values),
	}
	name := strings.ToLower(location)
	if protectedNamePart.MatchString(strings.NewReplacer("[", ".", "]", "", "$", "").Replace(name)) || strings.EqualFold(location, "Cookie") {
		proposal.Confidence = 1
		proposal.Reason = "protected security or business field"
		return proposal
	}
	training, holdout := values[:len(values)-1], values[len(values)-1:]
	pattern, confidence, description := commonPattern(training, name)
	if pattern == "" {
		proposal.Reason = "values vary but no narrow deterministic pattern was proven"
		return proposal
	}
	compiled, err := regexp.Compile(pattern)
	if err != nil || !compiled.MatchString(holdout[0]) {
		proposal.Pattern = pattern
		proposal.Confidence = confidence
		proposal.Reason = "candidate pattern failed validation on the holdout observation"
		return proposal
	}
	proposal.Pattern = pattern
	proposal.Confidence = confidence
	proposal.Decision = "accepted"
	proposal.Reason = description
	return proposal
}

func commonPattern(values []string, location string) (string, float64, string) {
	all := func(predicate func(string) bool) bool {
		for _, value := range values {
			if value == "" || !predicate(value) {
				return false
			}
		}
		return true
	}
	if volatileNamePart.MatchString(location) && all(uuidPattern.MatchString) {
		return `(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`, 0.99, "all observations and holdout values are UUIDs"
	}
	if volatileNamePart.MatchString(location) && all(rfc3339Pattern.MatchString) {
		return `^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})$`, 0.99, "all observations and holdout values are RFC3339 timestamps"
	}
	if volatileNamePart.MatchString(location) && all(integerPattern.MatchString) {
		return `^-?[0-9]{10,19}$`, 0.97, "volatile field name and all observations are integer timestamps or counters"
	}
	if volatileNamePart.MatchString(location) && all(hexPattern.MatchString) {
		return `(?i)^[0-9a-f]{16,128}$`, 0.98, "volatile field name and all observations are fixed-form hexadecimal values"
	}
	if volatileNamePart.MatchString(location) && all(base64URLPattern.MatchString) {
		return `^[A-Za-z0-9_-]{20,}={0,2}$`, 0.96, "volatile field name and all observations are base64url-like values"
	}
	return "", 0, ""
}

func ambiguityCount(config matcher.Config, events []event.Event) (int, error) {
	semanticMatcher, err := matcher.New(config)
	if err != nil {
		return 0, fmt.Errorf("validate proposed matcher: %w", err)
	}
	ambiguities := 0
	for _, requestEvent := range events {
		parsed, err := url.Parse(requestEvent.URL)
		if err != nil {
			continue
		}
		body, _ := base64.StdEncoding.DecodeString(requestEvent.BodyB64)
		request := &http.Request{Method: requestEvent.Method, URL: parsed, Host: parsed.Host, Header: http.Header(requestEvent.Headers)}
		var matching []event.Event
		for _, candidate := range events {
			if ok, _ := semanticMatcher.Match(candidate, request, body); ok {
				matching = append(matching, candidate)
			}
		}
		if len(matching) <= 1 || equivalentResponses(matching) {
			continue
		}
		ambiguities++
	}
	return ambiguities, nil
}

func equivalentResponses(events []event.Event) bool {
	if len(events) < 2 {
		return true
	}
	first := responseFingerprint(events[0])
	for _, captured := range events[1:] {
		if responseFingerprint(captured) != first {
			return false
		}
	}
	return true
}

func responseFingerprint(captured event.Event) string {
	value := struct {
		Status   int
		Headers  map[string][]string
		Trailers map[string][]string
		Body     string
		GRPC     string
	}{captured.Status, captured.ResponseHeaders, captured.ResponseTrailers, captured.ResponseBodySha256, captured.GrpcStatus}
	data, _ := json.Marshal(value)
	return hashBytes(data)
}

func mergeGeneratedRules(existing, generated []matcher.Rule) []matcher.Rule {
	byName := make(map[string]matcher.Rule, len(generated))
	for _, rule := range generated {
		byName[rule.Name] = rule
	}
	result := make([]matcher.Rule, 0, len(existing)+len(generated))
	for _, rule := range existing {
		if strings.HasPrefix(rule.Name, "heal-") {
			continue
		}
		result = append(result, rule)
	}
	names := make([]string, 0, len(byName))
	for name := range byName {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		result = append(result, byName[name])
	}
	return result
}

func normalizedPathRegex(path string) string {
	return "^" + regexp.QuoteMeta(path) + "$"
}

func generatedRuleName(method, host, path string) string {
	base := strings.ToLower(method + "-" + host + "-" + path)
	base = regexp.MustCompile(`[^a-z0-9]+`).ReplaceAllString(base, "-")
	base = strings.Trim(base, "-")
	if len(base) > 48 {
		base = base[:48]
	}
	suffix := hashBytes([]byte(strings.ToUpper(method) + "\x00" + strings.ToLower(host) + "\x00" + normalizedPathRegex(path)))[:8]
	return "heal-" + base + "-" + suffix
}

func flattenJSON(body []byte) map[string]string {
	if len(body) == 0 {
		return nil
	}
	var value any
	if err := json.Unmarshal(body, &value); err != nil {
		return nil
	}
	result := make(map[string]string)
	var walk func(any, string)
	walk = func(current any, path string) {
		switch typed := current.(type) {
		case map[string]any:
			keys := make([]string, 0, len(typed))
			for key := range typed {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, key := range keys {
				walk(typed[key], path+"."+key)
			}
		case []any:
			for index, child := range typed {
				walk(child, path+"["+strconv.Itoa(index)+"]")
			}
		default:
			encoded, _ := json.Marshal(typed)
			result[path] = strings.Trim(string(encoded), `"`)
		}
	}
	walk(value, "$")
	return result
}

func commonHeaderNames(items []observation) []string {
	if len(items) == 0 {
		return nil
	}
	counts := make(map[string]int)
	for _, item := range items {
		seen := make(map[string]bool)
		for name := range item.event.Headers {
			canonical := http.CanonicalHeaderKey(name)
			if !seen[canonical] {
				counts[canonical]++
				seen[canonical] = true
			}
		}
	}
	return commonNames(counts, len(items))
}

func commonQueryNames(items []observation) []string {
	counts := make(map[string]int)
	for _, item := range items {
		parsed, err := url.Parse(item.event.URL)
		if err != nil {
			continue
		}
		for name := range parsed.Query() {
			counts[name]++
		}
	}
	return commonNames(counts, len(items))
}

func commonJSONPaths(items []observation) []string {
	counts := make(map[string]int)
	for _, item := range items {
		for path := range item.json {
			counts[path]++
		}
	}
	return commonNames(counts, len(items))
}

func commonNames(counts map[string]int, total int) []string {
	var names []string
	for name, count := range counts {
		if count == total {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

func headerValue(headers map[string][]string, name string) string {
	for actual, values := range headers {
		if strings.EqualFold(actual, name) {
			return strings.Join(values, "\x00")
		}
	}
	return ""
}

func varies(values []string) bool {
	if len(values) < 2 {
		return false
	}
	for _, value := range values[1:] {
		if value != values[0] {
			return true
		}
	}
	return false
}

func hashValues(values []string) string {
	copyValues := append([]string(nil), values...)
	sort.Strings(copyValues)
	return hashBytes([]byte(strings.Join(copyValues, "\x00")))
}

func hashBytes(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

func writeAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".infernosim-heal-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(tempPath, path)
}

func writeReports(directory string, result Result) error {
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	jsonData, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return err
	}
	if err := writeAtomic(filepath.Join(directory, "infernosim-healing-report.json"), append(jsonData, '\n')); err != nil {
		return err
	}
	var html bytes.Buffer
	if err := reportTemplate.Execute(&html, result); err != nil {
		return err
	}
	return writeAtomic(filepath.Join(directory, "infernosim-healing-report.html"), html.Bytes())
}

var reportTemplate = template.Must(template.New("healing").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width">
<title>InfernoSIM healing report</title><style>
body{font-family:system-ui,sans-serif;max-width:1100px;margin:2rem auto;padding:0 1rem;color:#1c1d21}
table{border-collapse:collapse;width:100%}th,td{border:1px solid #d8dbe2;padding:.55rem;text-align:left;vertical-align:top}
th{background:#f4f5f7}.accepted{color:#08783e}.rejected{color:#a52a2a}code{overflow-wrap:anywhere}
</style></head><body><h1>InfernoSIM explainable healing</h1>
<p>Accepted: {{.Accepted}} · Rejected: {{.Rejected}} · Ambiguities: {{.Ambiguities}} · Input: <code>{{.InputHash}}</code></p>
<table><thead><tr><th>Decision</th><th>Rule</th><th>Field</th><th>Confidence</th><th>Reason</th></tr></thead><tbody>
{{range .Proposals}}<tr><td class="{{.Decision}}">{{.Decision}}</td><td><code>{{.Rule}}</code></td><td><code>{{.Kind}} {{.Location}}</code></td><td>{{printf "%.2f" .Confidence}}</td><td>{{.Reason}}</td></tr>{{end}}
</tbody></table></body></html>`))
