package snapshot

import (
	"regexp"
	"strings"
	"unicode"
)

// Transform is the entry point for creating the built-in transformers, mirroring the
// TransformerUtility of the python library:
//
//	snap.AddTransformer(snapshot.Transform.KeyValue("QueueUrl", "", true))
//
// The transformer types themselves are exported as well, in case you want to construct them
// directly with struct literals.
var Transform = TransformerUtility{}

// TransformerUtility groups the constructors for the built-in transformers.
type TransformerUtility struct{}

// KeyValue creates a transformer that replaces the value of every occurrence of key.
//
// An empty valueReplacement defaults to the key name in lowercase, separated by hyphens
// ("FunctionName" -> "function-name").
//
// With referenceReplacement, every occurrence of the matched value in the whole snapshot is replaced
// by a numbered placeholder (`<function-name:1>`) instead of only the value at that key.
func (TransformerUtility) KeyValue(key, valueReplacement string, referenceReplacement bool) Transformer {
	replacement := valueReplacement
	if replacement == "" {
		replacement = camelToHyphen(key)
	}
	return &KeyValueBasedTransformer{
		MatchFn:          matchKey(key),
		ReplacementFn:    func(string, any) string { return replacement },
		ReplaceReference: referenceReplacement,
	}
}

// KeyValueFunc is like KeyValue, but the replacement is computed from the matched key and value.
func (TransformerUtility) KeyValueFunc(key string, replacementFn ReplacementFunc, referenceReplacement bool) Transformer {
	if replacementFn == nil {
		replacement := camelToHyphen(key)
		replacementFn = func(string, any) string { return replacement }
	}
	return &KeyValueBasedTransformer{
		MatchFn:          matchKey(key),
		ReplacementFn:    replacementFn,
		ReplaceReference: referenceReplacement,
	}
}

// KeyValueMatch creates a transformer with a custom match function. The match function returns the
// part of the value that should be replaced, or ok=false if the pair does not match.
func (TransformerUtility) KeyValueMatch(matchFn MatchFunc, replacement string, referenceReplacement bool) Transformer {
	return &KeyValueBasedTransformer{
		MatchFn:          matchFn,
		ReplacementFn:    func(string, any) string { return replacement },
		ReplaceReference: referenceReplacement,
	}
}

// JSONPath creates a transformer that replaces the values matched by a json path.
func (TransformerUtility) JSONPath(jsonPath, valueReplacement string, referenceReplacement bool) Transformer {
	return &JSONPathTransformer{
		JSONPath:         jsonPath,
		Replacement:      valueReplacement,
		ReplaceReference: referenceReplacement,
	}
}

// Regex creates a transformer that replaces all matches of pattern in the serialized snapshot.
// The pattern uses Go's RE2 syntax, and capture groups are referenced as `$1` in the replacement.
// An invalid pattern panics, like regexp.MustCompile.
func (TransformerUtility) Regex(pattern, replacement string) Transformer {
	return &RegexTransformer{Regex: regexp.MustCompile(pattern), Replacement: replacement}
}

// RegexCompiled is Regex with a pre-compiled pattern.
func (TransformerUtility) RegexCompiled(pattern *regexp.Regexp, replacement string) Transformer {
	return &RegexTransformer{Regex: pattern, Replacement: replacement}
}

// Text creates a transformer that replaces all literal occurrences of text in the serialized
// snapshot. Useful if the text contains characters that would confuse a regex, like '+' or '('.
func (TransformerUtility) Text(text, replacement string) Transformer {
	return &TextTransformer{Text: text, Replacement: replacement}
}

// JSONString creates a transformer that parses the JSON string at key into a real object or list.
func (TransformerUtility) JSONString(key string) Transformer {
	return &JSONStringTransformer{Key: key}
}

// Sorting creates a transformer that sorts the list at key. A nil less function sorts by the
// canonical JSON representation of the items.
func (TransformerUtility) Sorting(key string, less func(a, b any) bool) Transformer {
	return &SortingTransformer{Key: key, Less: less}
}

// SortingByKey sorts the list at key by the string value of one of the fields of its items.
func (TransformerUtility) SortingByKey(key, itemKey string) Transformer {
	return &SortingTransformer{Key: key, Less: func(a, b any) bool {
		return canonicalString(itemValue(a, itemKey)) < canonicalString(itemValue(b, itemKey))
	}}
}

// Timestamp creates a transformer that replaces timestamps with a fixed reference timestamp of the
// same format.
func (TransformerUtility) Timestamp() Transformer {
	return NewTimestampTransformer()
}

// ResponseMetadata creates a transformer that reduces AWS SDK response metadata to the status code
// and a small set of headers.
func (TransformerUtility) ResponseMetadata() Transformer {
	return &ResponseMetadataTransformer{}
}

// Generic creates a transformer from a plain function.
func (TransformerUtility) Generic(fn func(input map[string]any, ctx *TransformContext) map[string]any) Transformer {
	return TransformerFunc(fn)
}

func itemValue(item any, key string) any {
	if m, ok := asMap(item); ok {
		return m[key]
	}
	return item
}

// matchKey matches a key exactly, as long as the value is neither nil nor an empty string.
func matchKey(key string) MatchFunc {
	return func(k string, v any) (any, bool) {
		if k != key || v == nil || v == "" {
			return nil, false
		}
		return v, true
	}
}

// camelToHyphen converts "FunctionName" to "function-name".
func camelToHyphen(input string) string {
	var builder strings.Builder
	for _, char := range input {
		if unicode.IsUpper(char) {
			builder.WriteRune('-')
			builder.WriteRune(unicode.ToLower(char))
			continue
		}
		builder.WriteRune(char)
	}
	return strings.Trim(builder.String(), "-")
}
