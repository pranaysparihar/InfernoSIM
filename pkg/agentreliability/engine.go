package agentreliability

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"infernosim/pkg/jsonpath"
)

type Request struct {
	Method  string
	Host    string
	Path    string
	Headers http.Header
	Body    []byte
}

type Response struct {
	Status  int
	Headers http.Header
	Body    []byte
}

type Decision struct {
	Response      Response
	Delay         time.Duration
	Timeout       time.Duration
	Reset         bool
	ResponseLost  bool
	AppliedFaults []string
}

type Operation struct {
	Kind      string `json:"kind"`
	Provider  string `json:"provider,omitempty"`
	Tool      string `json:"tool,omitempty"`
	CallID    string `json:"call_id,omitempty"`
	Arguments any    `json:"-"`
	Raw       any    `json:"-"`
}

type CallRecord struct {
	Sequence               int      `json:"sequence"`
	Kind                   string   `json:"kind"`
	Provider               string   `json:"provider,omitempty"`
	Tool                   string   `json:"tool,omitempty"`
	CallID                 string   `json:"call_id,omitempty"`
	SatisfiedVerifications []string `json:"satisfied_verifications,omitempty"`
}

type EffectRecord struct {
	Sequence          int    `json:"sequence"`
	Effect            string `json:"effect"`
	Tool              string `json:"tool,omitempty"`
	IdentityHash      string `json:"identity_hash"`
	IdempotencyHash   string `json:"idempotency_hash,omitempty"`
	Committed         bool   `json:"committed"`
	Deduplicated      bool   `json:"deduplicated,omitempty"`
	ResponseDelivered bool   `json:"response_delivered"`
	ResponseLost      bool   `json:"response_lost"`
}

type Snapshot struct {
	Version       int            `json:"version"`
	Calls         []CallRecord   `json:"calls,omitempty"`
	Effects       []EffectRecord `json:"effects,omitempty"`
	AppliedFaults []string       `json:"applied_faults,omitempty"`
	LimitExceeded bool           `json:"limit_exceeded"`
}

type compiledSelector struct {
	value Selector
	host  *regexp.Regexp
	path  *regexp.Regexp
}

type compiledProvider struct {
	value ProviderAdapter
	host  *regexp.Regexp
	path  *regexp.Regexp
}

type compiledEffect struct {
	value    Effect
	selector compiledSelector
}

type compiledFault struct {
	value    Fault
	selector compiledSelector
	delay    time.Duration
	timeout  time.Duration
}

type Engine struct {
	config      Config
	providers   []compiledProvider
	effects     []compiledEffect
	faults      []compiledFault
	activeFault map[string]bool

	mu                sync.Mutex
	sequence          int
	calls             []CallRecord
	effectRecords     []EffectRecord
	appliedFaults     []string
	faultOccurrences  map[string]int
	idempotencyCommit map[string]struct{}
	limitExceeded     bool
}

func NewEngine(config Config, activeFaultIDs []string) (*Engine, error) {
	config.ApplyDefaults()
	if err := config.Validate(); err != nil {
		return nil, err
	}
	engine := &Engine{
		config:            config,
		activeFault:       make(map[string]bool),
		faultOccurrences:  make(map[string]int),
		idempotencyCommit: make(map[string]struct{}),
	}
	known := make(map[string]struct{}, len(config.Faults))
	for _, provider := range config.Adapters.Providers {
		compiled := compiledProvider{value: provider}
		compiled.host, _ = compileOptional(provider.HostRegex)
		compiled.path, _ = compileOptional(provider.PathRegex)
		engine.providers = append(engine.providers, compiled)
	}
	for _, effect := range config.Effects {
		compiled, err := compileSelector(effect.Select)
		if err != nil {
			return nil, err
		}
		engine.effects = append(engine.effects, compiledEffect{value: effect, selector: compiled})
	}
	for _, fault := range config.Faults {
		known[fault.ID] = struct{}{}
		selector, err := compileSelector(fault.Select)
		if err != nil {
			return nil, err
		}
		delay, _ := parseBoundedDuration("delay", fault.Delay, true)
		timeout, _ := parseBoundedDuration("timeout", fault.Timeout, false)
		engine.faults = append(engine.faults, compiledFault{value: fault, selector: selector, delay: delay, timeout: timeout})
	}
	for _, id := range activeFaultIDs {
		if _, exists := known[id]; !exists {
			return nil, fmt.Errorf("unknown agent fault %q", id)
		}
		engine.activeFault[id] = true
	}
	return engine, nil
}

func compileSelector(value Selector) (compiledSelector, error) {
	compiled := compiledSelector{value: value}
	var err error
	compiled.host, err = compileOptional(value.HostRegex)
	if err != nil {
		return compiled, err
	}
	compiled.path, err = compileOptional(value.PathRegex)
	return compiled, err
}

func compileOptional(value string) (*regexp.Regexp, error) {
	if value == "" {
		return nil, nil
	}
	return regexp.Compile(value)
}

func (e *Engine) Reset() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.sequence = 0
	e.calls = nil
	e.effectRecords = nil
	e.appliedFaults = nil
	e.faultOccurrences = make(map[string]int)
	e.idempotencyCommit = make(map[string]struct{})
	e.limitExceeded = false
}

func (e *Engine) Process(request Request, response Response) (Decision, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(request.Body) > e.config.Limits.MaxBodyBytes || len(response.Body) > e.config.Limits.MaxBodyBytes {
		return Decision{}, fmt.Errorf("agent exchange exceeds configured body limit")
	}
	e.sequence++
	if e.sequence > e.config.Limits.MaxCalls {
		e.limitExceeded = true
		return Decision{}, fmt.Errorf("agent call limit exceeded")
	}
	requestOperation := e.inspectRequest(request)
	responseOperation := inspectResponse(requestOperation, response.Body)
	operation := responseOperation
	if operation.Tool == "" {
		operation.Tool = requestOperation.Tool
	}
	if operation.CallID == "" {
		operation.CallID = requestOperation.CallID
	}
	if operation.Provider == "" {
		operation.Provider = requestOperation.Provider
	}
	e.calls = append(e.calls, CallRecord{
		Sequence: e.sequence, Kind: operation.Kind, Provider: operation.Provider,
		Tool: operation.Tool, CallID: operation.CallID,
	})
	effectIndexes := e.recordEffects(request, requestOperation)
	decision := Decision{Response: cloneResponse(response)}
	for _, fault := range e.faults {
		if !e.activeFault[fault.value.ID] || !fault.selector.matches(request, operation) {
			continue
		}
		e.faultOccurrences[fault.value.ID]++
		if wanted := fault.value.Select.Occurrence; wanted > 0 && e.faultOccurrences[fault.value.ID] != wanted {
			continue
		}
		if err := applyFault(&decision, fault.value); err != nil {
			return Decision{}, fmt.Errorf("apply agent fault %q: %w", fault.value.ID, err)
		}
		decision.Delay += fault.delay
		decision.Timeout = fault.timeout
		decision.Reset = fault.value.Reset
		decision.ResponseLost = fault.value.CommittedResponseLost
		decision.AppliedFaults = append(decision.AppliedFaults, fault.value.ID)
		e.appliedFaults = append(e.appliedFaults, fault.value.ID)
	}
	if decision.ResponseLost && len(effectIndexes) == 0 {
		return Decision{}, fmt.Errorf("committed_response_lost matched a call without a declared effect")
	}
	for _, index := range effectIndexes {
		e.effectRecords[index].ResponseLost = decision.ResponseLost || decision.Reset || decision.Timeout > 0
		e.effectRecords[index].ResponseDelivered = !e.effectRecords[index].ResponseLost
	}
	if !decision.ResponseLost && !decision.Reset && decision.Timeout == 0 && decision.Response.Status >= 200 && decision.Response.Status < 300 {
		e.markVerifications(len(e.calls)-1, operation.Tool, decision.Response.Body)
	}
	return decision, nil
}

// markVerifications stores only assertion IDs whose configured predicate was
// satisfied. Tool responses and compared values never enter the public proof.
func (e *Engine) markVerifications(callIndex int, tool string, body []byte) {
	if tool == "" || callIndex < 0 || callIndex >= len(e.calls) {
		return
	}
	var root any
	parsed := json.Unmarshal(body, &root) == nil
	for _, assertion := range e.config.Assertions {
		if assertion.Type != "require_verification_before_retry" || assertion.VerificationTool != tool {
			continue
		}
		satisfied := assertion.VerificationPath == ""
		if assertion.VerificationPath != "" && parsed {
			value, exists := jsonpath.Get(root, assertion.VerificationPath)
			satisfied = exists
			if satisfied && assertion.VerificationValue != nil {
				left, _ := json.Marshal(value)
				right, _ := json.Marshal(assertion.VerificationValue)
				satisfied = bytes.Equal(left, right)
			}
		}
		if satisfied {
			e.calls[callIndex].SatisfiedVerifications = append(e.calls[callIndex].SatisfiedVerifications, assertion.ID)
		}
	}
}

func cloneResponse(response Response) Response {
	return Response{Status: response.Status, Headers: response.Headers.Clone(), Body: append([]byte(nil), response.Body...)}
}

func (e *Engine) inspectRequest(request Request) Operation {
	operation := Operation{Kind: "http_response"}
	var body any
	if len(request.Body) > 0 {
		_ = json.Unmarshal(request.Body, &body)
	}
	operation.Raw = body
	if e.config.Adapters.MCP {
		if object, ok := body.(map[string]any); ok {
			method, _ := object["method"].(string)
			switch method {
			case "tools/call":
				operation.Kind = "mcp_tool_result"
				params, _ := object["params"].(map[string]any)
				operation.Tool, _ = params["name"].(string)
				operation.Arguments = params["arguments"]
				operation.CallID = scalarString(object["id"])
				return operation
			case "tools/list":
				operation.Kind = "mcp_tools_list"
				operation.CallID = scalarString(object["id"])
				return operation
			}
		}
	}
	for _, provider := range e.providers {
		if provider.matches(request.Host, request.Path) {
			operation.Kind = "llm_response"
			operation.Provider = provider.value.Name
			return operation
		}
	}
	return operation
}

func inspectResponse(operation Operation, body []byte) Operation {
	result := operation
	var root any
	if json.Unmarshal(body, &root) != nil {
		return result
	}
	result.Raw = root
	if operation.Kind == "mcp_tool_result" || operation.Kind == "mcp_tools_list" {
		return result
	}
	if operation.Kind != "llm_response" {
		return result
	}
	// OpenAI Responses API.
	if output, ok := jsonpath.Get(root, "$.output"); ok {
		if entries, ok := output.([]any); ok {
			for _, entry := range entries {
				object, _ := entry.(map[string]any)
				if object["type"] == "function_call" {
					result.Tool, _ = object["name"].(string)
					result.CallID = scalarString(object["call_id"])
					return result
				}
			}
		}
	}
	// OpenAI Chat Completions.
	if value, ok := jsonpath.Get(root, "$.choices[0].message.tool_calls[0].function.name"); ok {
		result.Tool = scalarString(value)
		if id, found := jsonpath.Get(root, "$.choices[0].message.tool_calls[0].id"); found {
			result.CallID = scalarString(id)
		}
		return result
	}
	// Anthropic Messages.
	if content, ok := jsonpath.Get(root, "$.content"); ok {
		if entries, ok := content.([]any); ok {
			for _, entry := range entries {
				object, _ := entry.(map[string]any)
				if object["type"] == "tool_use" {
					result.Tool = scalarString(object["name"])
					result.CallID = scalarString(object["id"])
					return result
				}
			}
		}
	}
	// Ollama chat tool calls.
	if value, ok := jsonpath.Get(root, "$.message.tool_calls[0].function.name"); ok {
		result.Tool = scalarString(value)
		if id, found := jsonpath.Get(root, "$.message.tool_calls[0].id"); found {
			result.CallID = scalarString(id)
		}
		return result
	}
	return result
}

func (e *Engine) recordEffects(request Request, operation Operation) []int {
	identityRoot := map[string]any{"arguments": operation.Arguments, "body": operation.Raw}
	var indexes []int
	for _, effect := range e.effects {
		if !effect.selector.matches(request, operation) {
			continue
		}
		identityHash := hashSelected(identityRoot, effect.value.Identity)
		idempotencyHash := hashSelected(identityRoot, effect.value.DeduplicateBy)
		tool := operation.Tool
		if tool == "" {
			tool = effect.value.Name
		}
		record := EffectRecord{
			Sequence: e.sequence, Effect: effect.value.Name, Tool: tool,
			IdentityHash: identityHash, IdempotencyHash: idempotencyHash, Committed: true,
		}
		if len(effect.value.DeduplicateBy) > 0 {
			key := effect.value.Name + "\x00" + idempotencyHash
			if _, exists := e.idempotencyCommit[key]; exists {
				record.Committed = false
				record.Deduplicated = true
			} else {
				e.idempotencyCommit[key] = struct{}{}
			}
		}
		e.effectRecords = append(e.effectRecords, record)
		indexes = append(indexes, len(e.effectRecords)-1)
	}
	return indexes
}

func hashSelected(root any, paths []string) string {
	values := make([]any, 0, len(paths))
	for _, path := range paths {
		value, exists := jsonpath.Get(root, path)
		values = append(values, map[string]any{"path": path, "exists": exists, "value": value})
	}
	if len(paths) == 0 {
		values = append(values, root)
	}
	encoded, _ := json.Marshal(values)
	hash := sha256.Sum256(encoded)
	return hex.EncodeToString(hash[:16])
}

func applyFault(decision *Decision, fault Fault) error {
	if fault.Status != 0 {
		decision.Response.Status = fault.Status
	}
	if decision.Response.Headers == nil {
		decision.Response.Headers = make(http.Header)
	}
	for name, value := range fault.Headers {
		decision.Response.Headers.Set(name, value)
	}
	for _, mutation := range fault.Mutations {
		switch mutation.Operation {
		case "truncate":
			if mutation.MaxBytes < len(decision.Response.Body) {
				decision.Response.Body = append([]byte(nil), decision.Response.Body[:mutation.MaxBytes]...)
			}
		case "empty_success":
			decision.Response.Status = http.StatusOK
			decision.Response.Body = []byte(`{}`)
		default:
			var root any
			if err := json.Unmarshal(decision.Response.Body, &root); err != nil {
				contentType := strings.ToLower(decision.Response.Headers.Get("Content-Type"))
				if strings.Contains(contentType, "text/event-stream") || strings.Contains(contentType, "ndjson") || strings.Contains(contentType, "json-seq") {
					mutated, streamErr := mutateStructuredStream(decision.Response.Body, mutation, strings.Contains(contentType, "text/event-stream"))
					if streamErr != nil {
						return streamErr
					}
					decision.Response.Body = mutated
					continue
				}
				return fmt.Errorf("mutation %s requires a JSON response: %w", mutation.Operation, err)
			}
			if err := mutateJSON(root, mutation); err != nil {
				return err
			}
			encoded, err := json.Marshal(root)
			if err != nil {
				return err
			}
			decision.Response.Body = encoded
		}
	}
	decision.Response.Headers.Del("Content-Length")
	return nil
}

func mutateStructuredStream(body []byte, mutation Mutation, sse bool) ([]byte, error) {
	lines := bytes.Split(body, []byte("\n"))
	mutatedCount := 0
	for index, line := range lines {
		payload := bytes.TrimSuffix(line, []byte("\r"))
		prefix := []byte(nil)
		if sse {
			trimmed := bytes.TrimSpace(payload)
			if !bytes.HasPrefix(trimmed, []byte("data:")) {
				continue
			}
			prefixLength := bytes.Index(payload, []byte("data:")) + len("data:")
			prefix = append([]byte(nil), payload[:prefixLength]...)
			payload = bytes.TrimSpace(payload[prefixLength:])
			if bytes.Equal(payload, []byte("[DONE]")) {
				continue
			}
		} else {
			payload = bytes.TrimSpace(payload)
			if len(payload) == 0 {
				continue
			}
		}
		var root any
		if json.Unmarshal(payload, &root) != nil {
			continue
		}
		if err := mutateJSON(root, mutation); err != nil {
			continue
		}
		encoded, err := json.Marshal(root)
		if err != nil {
			return nil, err
		}
		if sse {
			encoded = append(append(prefix, ' '), encoded...)
		}
		if bytes.HasSuffix(line, []byte("\r")) {
			encoded = append(encoded, '\r')
		}
		lines[index] = encoded
		mutatedCount++
	}
	if mutatedCount == 0 {
		return nil, fmt.Errorf("mutation %s path %s matched no structured stream frame", mutation.Operation, mutation.Path)
	}
	return bytes.Join(lines, []byte("\n")), nil
}

func mutateJSON(root any, mutation Mutation) error {
	switch mutation.Operation {
	case "delete":
		if !jsonpath.Delete(root, mutation.Path) {
			return fmt.Errorf("delete path %s was not present", mutation.Path)
		}
	case "set":
		if !jsonpath.Set(root, mutation.Path, mutation.Value) {
			return fmt.Errorf("set path %s was not present", mutation.Path)
		}
	case "change_type":
		current, exists := jsonpath.Get(root, mutation.Path)
		if !exists {
			return fmt.Errorf("change_type path %s was not present", mutation.Path)
		}
		if !jsonpath.Set(root, mutation.Path, convertedValue(current, mutation.Type)) {
			return fmt.Errorf("change_type path %s could not be changed", mutation.Path)
		}
	case "duplicate":
		value, exists := jsonpath.Get(root, mutation.Path)
		array, ok := value.([]any)
		if !exists || !ok || len(array) == 0 {
			return fmt.Errorf("duplicate path %s must address a non-empty array", mutation.Path)
		}
		copyArray := append(append([]any(nil), array...), array[len(array)-1])
		if !jsonpath.Set(root, mutation.Path, copyArray) {
			return fmt.Errorf("duplicate path %s could not be changed", mutation.Path)
		}
	case "reverse":
		value, exists := jsonpath.Get(root, mutation.Path)
		array, ok := value.([]any)
		if !exists || !ok {
			return fmt.Errorf("reverse path %s must address an array", mutation.Path)
		}
		copyArray := append([]any(nil), array...)
		for left, right := 0, len(copyArray)-1; left < right; left, right = left+1, right-1 {
			copyArray[left], copyArray[right] = copyArray[right], copyArray[left]
		}
		if !jsonpath.Set(root, mutation.Path, copyArray) {
			return fmt.Errorf("reverse path %s could not be changed", mutation.Path)
		}
	default:
		return fmt.Errorf("unsupported mutation %q", mutation.Operation)
	}
	return nil
}

func convertedValue(value any, target string) any {
	switch target {
	case "string":
		return scalarString(value)
	case "number":
		switch typed := value.(type) {
		case float64:
			return typed
		case string:
			parsed, _ := strconv.ParseFloat(typed, 64)
			return parsed
		default:
			return float64(0)
		}
	case "boolean":
		return false
	case "null":
		return nil
	case "object":
		return map[string]any{}
	case "array":
		return []any{}
	default:
		return value
	}
}

func (s compiledSelector) matches(request Request, operation Operation) bool {
	if s.value.Kind != "" && s.value.Kind != "any" && s.value.Kind != operation.Kind {
		return false
	}
	if s.value.Provider != "" && s.value.Provider != operation.Provider {
		return false
	}
	if s.value.Tool != "" && s.value.Tool != operation.Tool {
		return false
	}
	if s.value.Method != "" && !strings.EqualFold(s.value.Method, request.Method) {
		return false
	}
	if s.host != nil && !s.host.MatchString(request.Host) {
		return false
	}
	return s.path == nil || s.path.MatchString(request.Path)
}

func (p compiledProvider) matches(host, path string) bool {
	return (p.host == nil || p.host.MatchString(host)) && (p.path == nil || p.path.MatchString(path))
}

func scalarString(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case nil:
		return ""
	default:
		encoded, _ := json.Marshal(typed)
		return string(encoded)
	}
}

func (e *Engine) Snapshot() Snapshot {
	e.mu.Lock()
	defer e.mu.Unlock()
	return Snapshot{
		Version: 1, Calls: append([]CallRecord(nil), e.calls...),
		Effects:       append([]EffectRecord(nil), e.effectRecords...),
		AppliedFaults: append([]string(nil), e.appliedFaults...), LimitExceeded: e.limitExceeded,
	}
}

type AssertionResult struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Passed  bool   `json:"passed"`
	Message string `json:"message"`
}

func Evaluate(config Config, snapshot Snapshot, unexpectedCalls bool, elapsed time.Duration) []AssertionResult {
	results := make([]AssertionResult, 0, len(config.Assertions))
	for _, assertion := range config.Assertions {
		if !assertionApplies(assertion, snapshot.AppliedFaults) {
			continue
		}
		result := AssertionResult{ID: assertion.ID, Type: assertion.Type, Passed: true}
		switch assertion.Type {
		case "exactly_once_effect", "at_most_once_effect", "forbidden_effect":
			committed := 0
			for _, effect := range snapshot.Effects {
				if effect.Effect == assertion.Effect && effect.Committed {
					committed++
				}
			}
			switch assertion.Type {
			case "exactly_once_effect":
				result.Passed = committed == 1
				result.Message = fmt.Sprintf("effect %s committed %d time(s); expected exactly 1", assertion.Effect, committed)
			case "at_most_once_effect":
				result.Passed = committed <= 1
				result.Message = fmt.Sprintf("effect %s committed %d time(s); expected at most 1", assertion.Effect, committed)
			case "forbidden_effect":
				result.Passed = committed == 0
				result.Message = fmt.Sprintf("forbidden effect %s committed %d time(s)", assertion.Effect, committed)
			}
		case "max_calls":
			count := 0
			for _, call := range snapshot.Calls {
				if call.Tool == assertion.Tool && call.Kind != "llm_response" {
					count++
				}
			}
			result.Passed = count <= assertion.Max
			result.Message = fmt.Sprintf("tool %s called %d time(s); maximum %d", assertion.Tool, count, assertion.Max)
		case "no_unexpected_calls":
			result.Passed = !unexpectedCalls
			if unexpectedCalls {
				result.Message = "agent made an outbound call outside the recorded universe"
			} else {
				result.Message = "all agent outbound calls stayed within the recorded universe"
			}
		case "deadline":
			deadline, _ := time.ParseDuration(assertion.Duration)
			result.Passed = elapsed <= deadline
			result.Message = fmt.Sprintf("run took %s; deadline %s", elapsed.Round(time.Millisecond), deadline)
		case "require_verification_before_retry":
			result.Passed, result.Message = verificationResult(assertion, snapshot)
		}
		if result.Passed && result.Message == "" {
			result.Message = "assertion passed"
		}
		results = append(results, result)
	}
	return results
}

func assertionApplies(assertion Assertion, activeFaults []string) bool {
	if len(assertion.Faults) == 0 {
		return true
	}
	if len(activeFaults) == 0 {
		return assertion.IncludeBaseline
	}
	for _, wanted := range assertion.Faults {
		for _, active := range activeFaults {
			if wanted == active {
				return true
			}
		}
	}
	return false
}

func verificationResult(assertion Assertion, snapshot Snapshot) (bool, string) {
	for index, first := range snapshot.Effects {
		if first.Effect != assertion.Effect || !first.ResponseLost {
			continue
		}
		for _, retry := range snapshot.Effects[index+1:] {
			if retry.Effect != first.Effect || retry.IdentityHash != first.IdentityHash {
				continue
			}
			verified := false
			for _, call := range snapshot.Calls {
				if call.Sequence > first.Sequence && call.Sequence < retry.Sequence && call.Tool == assertion.VerificationTool && containsString(call.SatisfiedVerifications, assertion.ID) {
					verified = true
					break
				}
			}
			if !verified {
				return false, fmt.Sprintf("effect %s was retried after an ambiguous response without calling %s", assertion.Effect, assertion.VerificationTool)
			}
		}
	}
	return true, fmt.Sprintf("ambiguous %s retries were verified with %s", assertion.Effect, assertion.VerificationTool)
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func StableHash(value any) string {
	encoded, _ := json.Marshal(value)
	hash := sha256.Sum256(encoded)
	return hex.EncodeToString(hash[:])
}

func UniqueFaults(values []string) []string {
	seen := make(map[string]struct{})
	var result []string
	for _, value := range values {
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}
