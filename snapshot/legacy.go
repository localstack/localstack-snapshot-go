package snapshot

import "regexp"

// The functions in this file mirror the "legacy" API of the python library. They are thin wrappers
// around the transformers, kept for an easier port of existing snapshot tests.

// RegisterReplacement replaces every match of pattern in the serialized snapshot with value.
func (s *Session) RegisterReplacement(pattern *regexp.Regexp, value string) {
	s.AddTransformer(&RegexTransformer{Regex: pattern, Replacement: value})
}

// SkipKey replaces the value of every key matching pattern with value. Like the python variant, the
// pattern has to match at the start of the key.
func (s *Session) SkipKey(pattern *regexp.Regexp, value string) {
	s.AddTransformer(&KeyValueBasedTransformer{
		MatchFn: func(key string, v any) (any, bool) {
			if matchesAtStart(pattern, key) {
				return v, true
			}
			return nil, false
		},
		ReplacementFn:    func(string, any) string { return value },
		ReplaceReference: false,
	})
}

// ReplaceValue replaces every value matching pattern with value. Like the python variant, the pattern
// has to match at the start of the value.
func (s *Session) ReplaceValue(pattern *regexp.Regexp, value string) {
	s.AddTransformer(&KeyValueBasedTransformer{
		MatchFn: func(_ string, v any) (any, bool) {
			str, ok := v.(string)
			if ok && matchesAtStart(pattern, str) {
				return v, true
			}
			return nil, false
		},
		ReplacementFn:    func(string, any) string { return value },
		ReplaceReference: false,
	})
}

// matchesAtStart is the equivalent of python's re.match: the pattern has to match at position 0.
func matchesAtStart(pattern *regexp.Regexp, s string) bool {
	loc := pattern.FindStringIndex(s)
	return loc != nil && loc[0] == 0
}
