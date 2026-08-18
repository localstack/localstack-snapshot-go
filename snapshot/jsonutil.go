package snapshot

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// marshalValue serializes a value without escaping HTML characters. Escaping would mangle the
// `<placeholder:1>` tokens that transformers write into snapshots.
func marshalValue(v any) (string, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return "", err
	}
	return strings.TrimSuffix(buf.String(), "\n"), nil
}

// marshalIndented serializes a value for persisting to a snapshot file. The trailing newline keeps
// the file compatible with the end-of-file-fixer style pre-commit hooks.
func marshalIndented(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// unmarshalValue parses JSON into the snapshot value space (numbers become json.Number). Trailing
// content makes it fail, so that a string like `{"a": 1}trailing` is not mistaken for JSON.
func unmarshalValue(data string) (any, error) {
	if !json.Valid([]byte(data)) {
		return nil, fmt.Errorf("invalid JSON")
	}
	dec := json.NewDecoder(strings.NewReader(data))
	dec.UseNumber()
	var out any
	if err := dec.Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

// unmarshalMap parses a JSON object into the snapshot value space.
func unmarshalMap(data []byte) (map[string]any, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return map[string]any{}, nil
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	out := map[string]any{}
	if err := dec.Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

// asMap is a convenience type assertion used throughout the transformers.
func asMap(v any) (map[string]any, bool) {
	m, ok := v.(map[string]any)
	return m, ok
}

// asList is a convenience type assertion used throughout the transformers.
func asList(v any) ([]any, bool) {
	l, ok := v.([]any)
	return l, ok
}

// canonicalString renders a value as a stable string, used for sorting and equality checks.
func canonicalString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	out, err := marshalValue(v)
	if err != nil {
		return ""
	}
	return out
}
