package snapshot

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Transformer normalizes non-deterministic values (ids, arns, timestamps, ...) before a snapshot is
// recorded or compared. A transformer either rewrites the state in place, or registers a string
// replacement on the context, which is applied to the serialized snapshot afterwards.
type Transformer interface {
	Transform(input map[string]any, ctx *TransformContext) map[string]any
}

// TransformerFunc adapts a plain function to the Transformer interface.
type TransformerFunc func(input map[string]any, ctx *TransformContext) map[string]any

func (f TransformerFunc) Transform(input map[string]any, ctx *TransformContext) map[string]any {
	return f(input, ctx)
}

// ReplacementFunc computes a replacement string for a matched key/value pair.
type ReplacementFunc func(key string, value any) string

// MatchFunc reports whether a key/value pair should be transformed. The returned value is the part
// that gets replaced: return the whole value to replace all of it, or a substring to replace only
// that portion.
type MatchFunc func(key string, value any) (match any, ok bool)

// SerializedReplacement is applied to the JSON-serialized snapshot.
type SerializedReplacement func(string) string

// TransformContext is shared by all transformers of a single snapshot session. It collects the
// replacements that are applied to the serialized snapshot and hands out the numbers used in
// reference placeholders such as `<queue-url:1>`.
type TransformContext struct {
	replacements []SerializedReplacement
	scopedTokens map[string]int
	seenRefs     map[string]bool
	errs         []error
	logf         func(format string, args ...any)
}

// NewTransformContext creates an empty context. A session creates one per snapshot run.
func NewTransformContext() *TransformContext {
	return &TransformContext{
		scopedTokens: map[string]int{},
		seenRefs:     map[string]bool{},
	}
}

// SerializedReplacements returns the replacements registered so far, in registration order.
func (c *TransformContext) SerializedReplacements() []SerializedReplacement {
	return c.replacements
}

// RegisterSerializedReplacement adds a replacement that is applied to the serialized snapshot.
func (c *TransformContext) RegisterSerializedReplacement(fn SerializedReplacement) {
	c.replacements = append(c.replacements, fn)
}

// NewScope returns the next number for a given placeholder scope, e.g. 2 for `<resource:2>`.
func (c *TransformContext) NewScope(scope string) int {
	if c.scopedTokens == nil {
		c.scopedTokens = map[string]int{}
	}
	c.scopedTokens[scope]++
	return c.scopedTokens[scope]
}

// Errorf records a transformer error. Errors fail the surrounding test instead of panicking.
func (c *TransformContext) Errorf(format string, args ...any) {
	c.errs = append(c.errs, fmt.Errorf(format, args...))
}

// Err returns the first error recorded by a transformer, if any.
func (c *TransformContext) Err() error {
	if len(c.errs) == 0 {
		return nil
	}
	return c.errs[0]
}

// Debugf logs a message when snapshot debugging is enabled (DEBUG_SNAPSHOT).
func (c *TransformContext) Debugf(format string, args ...any) {
	if c == nil || c.logf == nil {
		return
	}
	c.logf(format, args...)
}

// registerReferenceReplacement registers a numbered placeholder for every occurrence of a value in
// the snapshot, e.g. all occurrences of a queue url become `<queue-url:1>`.
func registerReferenceReplacement(ctx *TransformContext, referenceValue any, replacement string) {
	value, ok := referenceValue.(string)
	if !ok {
		ctx.Errorf(
			"the reference value %v of type %T is not a string; use a transformer without reference "+
				"replacement for the replacement %q, reference replacements only support strings",
			referenceValue, referenceValue, replacement,
		)
		return
	}

	value = strings.ReplaceAll(value, `"`, `\"`)
	if ctx.seenRefs == nil {
		ctx.seenRefs = map[string]bool{}
	}
	if ctx.seenRefs[value] {
		return
	}
	ctx.seenRefs[value] = true

	placeholder := fmt.Sprintf("<%s:%d>", replacement, ctx.NewScope(replacement))
	ctx.Debugf("registering reference replacement for value %q -> %q", truncate(value, 200), placeholder)
	ctx.RegisterSerializedReplacement(func(s string) string {
		return strings.ReplaceAll(s, value, placeholder)
	})
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

// KeyValueBasedTransformer replaces values whose key/value pair matches MatchFn.
type KeyValueBasedTransformer struct {
	MatchFn MatchFunc
	// ReplacementFn computes the replacement. Required.
	ReplacementFn ReplacementFunc
	// ReplaceReference replaces every occurrence of the matched value in the whole snapshot with a
	// numbered placeholder instead of only the matched location.
	ReplaceReference bool
}

func (t *KeyValueBasedTransformer) Transform(input map[string]any, ctx *TransformContext) map[string]any {
	if t.MatchFn == nil || t.ReplacementFn == nil {
		ctx.Errorf("key value transformer needs both a MatchFn and a ReplacementFn")
		return input
	}

	for _, key := range sortedKeys(input) {
		value := input[key]

		if match, ok := t.MatchFn(key, value); ok {
			replacement := t.ReplacementFn(key, value)
			if t.ReplaceReference {
				registerReferenceReplacement(ctx, match, replacement)
				continue
			}

			if str, isString := value.(string); isString {
				if matchStr, isString := match.(string); isString {
					ctx.Debugf("replacing value for key %q: match %q with %q", key, truncate(matchStr, 200), replacement)
					input[key] = strings.ReplaceAll(str, matchStr, replacement)
					continue
				}
			}
			ctx.Debugf("replacing value for key %q with %q", key, replacement)
			input[key] = replacement
			continue
		}

		if list, ok := asList(value); ok {
			for i, item := range list {
				if nested, ok := asMap(item); ok {
					list[i] = t.Transform(nested, ctx)
				}
			}
			continue
		}

		if nested, ok := asMap(value); ok {
			input[key] = t.Transform(nested, ctx)
		}
	}
	return input
}

// JSONPathTransformer replaces the values matched by a json path.
type JSONPathTransformer struct {
	JSONPath         string
	Replacement      string
	ReplaceReference bool
}

func (t *JSONPathTransformer) Transform(input map[string]any, ctx *TransformContext) map[string]any {
	path, err := parseJSONPath(t.JSONPath)
	if err != nil {
		ctx.Errorf("invalid json path %q: %w", t.JSONPath, err)
		return input
	}

	matches := path.find(input)
	if len(matches) == 0 {
		ctx.Debugf("no match found for json path %q", t.JSONPath)
		return input
	}

	for _, match := range matches {
		if t.ReplaceReference {
			registerReferenceReplacement(ctx, match.value, t.Replacement)
			continue
		}
		ctx.Debugf("replacing json path %q with %q", t.JSONPath, t.Replacement)
		match.set(t.Replacement)
	}
	return input
}

// RegexTransformer replaces all regex matches in the serialized snapshot. Note that Go uses RE2
// syntax and `$1` (not `\1`) for capture group references in the replacement.
type RegexTransformer struct {
	Regex       *regexp.Regexp
	Replacement string
}

func (t *RegexTransformer) Transform(input map[string]any, ctx *TransformContext) map[string]any {
	if t.Regex == nil {
		ctx.Errorf("regex transformer for replacement %q has no regex", t.Replacement)
		return input
	}
	ctx.Debugf("registering regex pattern %q in snapshot with %q", truncate(t.Regex.String(), 200), t.Replacement)
	ctx.RegisterSerializedReplacement(func(s string) string {
		return t.Regex.ReplaceAllString(s, t.Replacement)
	})
	return input
}

// TextTransformer replaces all literal occurrences of a text in the serialized snapshot. Useful when
// the text contains characters that would need escaping in a regex, like '+' or '('.
type TextTransformer struct {
	Text        string
	Replacement string
}

func (t *TextTransformer) Transform(input map[string]any, ctx *TransformContext) map[string]any {
	ctx.Debugf("registering text pattern %q in snapshot with %q", t.Text, t.Replacement)
	ctx.RegisterSerializedReplacement(func(s string) string {
		return strings.ReplaceAll(s, t.Text, t.Replacement)
	})
	return input
}

// SortingTransformer sorts the list found at Key, so that a non-deterministic order does not fail
// the snapshot. Less defaults to comparing the canonical JSON representation of the items.
type SortingTransformer struct {
	Key  string
	Less func(a, b any) bool
}

func (t *SortingTransformer) Transform(input map[string]any, ctx *TransformContext) map[string]any {
	result, ok := asMap(t.transform(input, ctx))
	if !ok {
		return input
	}
	return result
}

func (t *SortingTransformer) transform(value any, ctx *TransformContext) any {
	switch container := value.(type) {
	case map[string]any:
		for _, key := range sortedKeys(container) {
			if key != t.Key {
				container[key] = t.transform(container[key], ctx)
				continue
			}
			list, ok := asList(container[key])
			if !ok {
				ctx.Errorf("sorting transformer for key %q should only be applied to lists, got %T", t.Key, container[key])
				continue
			}
			for i, item := range list {
				list[i] = t.transform(item, ctx)
			}
			less := t.Less
			if less == nil {
				less = func(a, b any) bool { return canonicalString(a) < canonicalString(b) }
			}
			sort.SliceStable(list, func(i, j int) bool { return less(list[i], list[j]) })
			container[key] = list
		}
		return container
	case []any:
		for i, item := range container {
			container[i] = t.transform(item, ctx)
		}
		return container
	default:
		return value
	}
}

// RegexMatcher pairs a timestamp pattern with the fixed value it is replaced by.
type RegexMatcher struct {
	Regex          *regexp.Regexp
	Representation string
}

// ReferenceDate is the timestamp all recorded timestamps are replaced with: the commit timestamp of
// the localstack v1.0.0 tag.
const ReferenceDate = "2022-07-13T13:48:01Z"

// TimestampTransformer replaces timestamp strings with a fixed representative value of the same
// format, e.g. `<timestamp:2022-07-13T13:48:01Z>`.
//
// Go time.Time values are normalized to a millisecond precision UTC string first (see Normalize), so
// they are matched as well.
type TimestampTransformer struct {
	Matchers []RegexMatcher
}

// NewTimestampTransformer creates a TimestampTransformer with the default matchers.
func NewTimestampTransformer() *TimestampTransformer {
	return &TimestampTransformer{Matchers: defaultTimestampMatchers()}
}

func defaultTimestampMatchers() []RegexMatcher {
	return []RegexMatcher{
		// stepfunctions internal, and Go time.Time normalized by Normalize
		{regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$`), "2022-07-13T13:48:01.000Z"},
		// lambda
		{regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}[+-]\d{4}$`), "2022-07-13T13:48:01.000+0000"},
		// stepfunctions external, also cloudformation
		{regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{6}[+-]\d{2}:\d{2}$`), "2022-07-13T13:48:01.000000+00:00"},
		// s3
		{regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z$`), ReferenceDate},
		// any other RFC3339 timestamp, e.g. time.Time serialized by encoding/json. All fractional
		// precisions map to the same representation, since Go drops trailing zeros.
		{regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d{1,9})?(Z|[+-]\d{2}:\d{2})$`), ReferenceDate},
	}
}

func (t *TimestampTransformer) Transform(input map[string]any, ctx *TransformContext) map[string]any {
	result, ok := asMap(t.transform(input))
	if !ok {
		return input
	}
	return result
}

func (t *TimestampTransformer) transform(value any) any {
	switch container := value.(type) {
	case map[string]any:
		for key, nested := range container {
			container[key] = t.transform(nested)
		}
		return container
	case []any:
		for i, item := range container {
			container[i] = t.transform(item)
		}
		return container
	case string:
		matchers := t.Matchers
		if matchers == nil {
			matchers = defaultTimestampMatchers()
		}
		for _, matcher := range matchers {
			if matcher.Regex.MatchString(container) {
				return fmt.Sprintf("<timestamp:%s>", matcher.Representation)
			}
		}
		return container
	default:
		return value
	}
}

// JSONStringTransformer parses the JSON string found at Key into a real object or list, so that
// other transformers can be applied to its contents.
//
// This complements the best-effort parsing that every snapshot goes through (which only handles
// strings starting with '{'): it also handles JSON lists and nested JSON strings.
type JSONStringTransformer struct {
	Key string
}

func (t *JSONStringTransformer) Transform(input map[string]any, ctx *TransformContext) map[string]any {
	result, ok := asMap(t.transform(input, ctx))
	if !ok {
		return input
	}
	return result
}

func (t *JSONStringTransformer) transform(value any, ctx *TransformContext) any {
	switch container := value.(type) {
	case map[string]any:
		for key, nested := range container {
			if key != t.Key {
				container[key] = t.transform(nested, ctx)
				continue
			}
			str, isString := nested.(string)
			if !isString || !looksLikeJSON(str) {
				container[key] = t.transform(nested, ctx)
				continue
			}
			parsed, err := unmarshalValue(str)
			if err != nil {
				ctx.Debugf("value mapped to key %q is not a valid JSON string and won't be transformed: %s", key, str)
				continue
			}
			ctx.Debugf("replacing string value of %q with parsed JSON", key)
			container[key] = parseNestedJSONStrings(parsed)
		}
		return container
	case []any:
		for i, item := range container {
			container[i] = t.transform(item, ctx)
		}
		return container
	default:
		return value
	}
}

// parseNestedJSONStrings recursively parses every string that looks like JSON. Best effort: values
// that fail to parse are left untouched.
func parseNestedJSONStrings(value any) any {
	switch container := value.(type) {
	case map[string]any:
		for key, nested := range container {
			container[key] = parseNestedJSONStrings(nested)
		}
		return container
	case []any:
		for i, item := range container {
			container[i] = parseNestedJSONStrings(item)
		}
		return container
	case string:
		if !looksLikeJSON(container) {
			return container
		}
		parsed, err := unmarshalValue(container)
		if err != nil {
			return container
		}
		return parseNestedJSONStrings(parsed)
	default:
		return value
	}
}

func looksLikeJSON(s string) bool {
	trimmed := strings.TrimSpace(s)
	return strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[")
}

// ResponseMetadataTransformer reduces the AWS SDK response metadata to the interesting bits: the
// status code and a small set of headers.
type ResponseMetadataTransformer struct{}

func (t *ResponseMetadataTransformer) Transform(input map[string]any, ctx *TransformContext) map[string]any {
	headersToCollect := []string{"content_type"}

	for key, value := range input {
		if key != "ResponseMetadata" {
			if nested, ok := asMap(value); ok {
				input[key] = t.Transform(nested, ctx)
			}
			continue
		}

		metadata, ok := asMap(value)
		if !ok {
			continue
		}
		httpHeaders, ok := asMap(metadata["HTTPHeaders"])
		if !ok {
			continue
		}

		simplifiedHeaders := map[string]any{}
		for _, header := range headersToCollect {
			if headerValue, ok := httpHeaders[header]; ok && headerValue != nil && headerValue != "" {
				simplifiedHeaders[header] = headerValue
			}
		}

		simplified := map[string]any{"HTTPHeaders": simplifiedHeaders}
		// HTTPStatusCode might have been removed by a skipped verification path.
		if statusCode, ok := metadata["HTTPStatusCode"]; ok {
			simplified["HTTPStatusCode"] = statusCode
		}
		input[key] = simplified
	}
	return input
}
