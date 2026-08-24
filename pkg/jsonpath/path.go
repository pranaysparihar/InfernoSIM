// Package jsonpath implements InfernoSIM's deliberately small, deterministic
// JSONPath subset. It supports object keys and zero-based array indexes, for
// example $.customer.id, $.orders[0].id, and $[1].result.
package jsonpath

import (
	"fmt"
	"strconv"
	"strings"
)

const (
	maxPathLength = 4096
	maxTokens     = 128
)

type token struct {
	key   string
	index int
	array bool
}

// Validate reports whether path belongs to InfernoSIM's bounded JSONPath
// subset. Wildcards, recursive descent, filters, and scripts are intentionally
// rejected so matching and mutation remain portable and reproducible.
func Validate(path string) error {
	_, err := parse(path)
	return err
}

// Get extracts the value at path.
func Get(root any, path string) (any, bool) {
	tokens, err := parse(path)
	if err != nil {
		return nil, false
	}
	current := root
	for _, item := range tokens {
		if item.array {
			array, ok := current.([]any)
			if !ok || item.index >= len(array) {
				return nil, false
			}
			current = array[item.index]
			continue
		}
		object, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		current, ok = object[item.key]
		if !ok {
			return nil, false
		}
	}
	return current, true
}

// Set replaces an existing value. It does not create missing parents, which
// prevents a typo in a fault rule from silently fabricating a new structure.
func Set(root any, path string, value any) bool {
	parent, last, ok := resolveParent(root, path)
	if !ok {
		return false
	}
	if last.array {
		array, ok := parent.([]any)
		if !ok || last.index >= len(array) {
			return false
		}
		array[last.index] = value
		return true
	}
	object, ok := parent.(map[string]any)
	if !ok {
		return false
	}
	if _, exists := object[last.key]; !exists {
		return false
	}
	object[last.key] = value
	return true
}

// Delete removes an object member. Array entries are replaced with null rather
// than shifted so downstream indexes and recorded trajectory shape stay stable.
func Delete(root any, path string) bool {
	parent, last, ok := resolveParent(root, path)
	if !ok {
		return false
	}
	if last.array {
		array, ok := parent.([]any)
		if !ok || last.index >= len(array) {
			return false
		}
		array[last.index] = nil
		return true
	}
	object, ok := parent.(map[string]any)
	if !ok {
		return false
	}
	if _, exists := object[last.key]; !exists {
		return false
	}
	delete(object, last.key)
	return true
}

func resolveParent(root any, path string) (any, token, bool) {
	tokens, err := parse(path)
	if err != nil || len(tokens) == 0 {
		return nil, token{}, false
	}
	current := root
	for _, item := range tokens[:len(tokens)-1] {
		if item.array {
			array, ok := current.([]any)
			if !ok || item.index >= len(array) {
				return nil, token{}, false
			}
			current = array[item.index]
			continue
		}
		object, ok := current.(map[string]any)
		if !ok {
			return nil, token{}, false
		}
		current, ok = object[item.key]
		if !ok {
			return nil, token{}, false
		}
	}
	return current, tokens[len(tokens)-1], true
}

func parse(path string) ([]token, error) {
	if len(path) == 0 || len(path) > maxPathLength {
		return nil, fmt.Errorf("JSONPath length must be between 1 and %d", maxPathLength)
	}
	if path[0] != '$' {
		return nil, fmt.Errorf("JSONPath must begin with $, $., or $[index]")
	}
	if len(path) == 1 {
		return nil, nil
	}
	var tokens []token
	for position := 1; position < len(path); {
		if len(tokens) >= maxTokens {
			return nil, fmt.Errorf("JSONPath exceeds %d components", maxTokens)
		}
		switch path[position] {
		case '.':
			position++
			start := position
			for position < len(path) && path[position] != '.' && path[position] != '[' {
				position++
			}
			key := path[start:position]
			if key == "" || strings.ContainsAny(key, "]*") {
				return nil, fmt.Errorf("invalid JSONPath object key %q", key)
			}
			tokens = append(tokens, token{key: key})
		case '[':
			closeOffset := strings.IndexByte(path[position:], ']')
			if closeOffset < 2 {
				return nil, fmt.Errorf("invalid JSONPath array index")
			}
			closeAt := position + closeOffset
			index, err := strconv.Atoi(path[position+1 : closeAt])
			if err != nil || index < 0 {
				return nil, fmt.Errorf("invalid JSONPath array index")
			}
			tokens = append(tokens, token{index: index, array: true})
			position = closeAt + 1
		default:
			return nil, fmt.Errorf("invalid JSONPath near %q", path[position:])
		}
	}
	return tokens, nil
}
