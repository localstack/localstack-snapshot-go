package snapshot

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// This file implements the subset of JSONPath that snapshot tests need, without pulling in a
// dependency. Supported syntax:
//
//	$.a.b            child selection
//	$..a             recursive descent
//	$.a[0] / $.a.0   list index (also works after a recursive descent: $..a..[0])
//	$.a[*] / $.a.*   wildcard
//	$.'a.b' / $['a.b'] / $["a.b"]   quoted keys, for keys containing dots or other specials
type selectorKind int

const (
	selectorName selectorKind = iota // plain key, or index when applied to a list
	selectorIndex
	selectorWildcard
)

type pathSegment struct {
	recursive bool // preceded by '..', so the selector applies at any depth
	kind      selectorKind
	name      string
	index     int
	// quoted keys are never interpreted as list indices
	quoted bool
}

type jsonPath struct {
	raw      string
	segments []pathSegment
}

// pathMatch is a single node matched by a jsonPath, including where it lives in its parent so that
// callers can update or delete it.
type pathMatch struct {
	// parent is the map[string]any or []any holding the value, nil for the root node.
	parent any
	key    string // set when parent is a map
	index  int    // set when parent is a list
	value  any
	path   []any // path elements from the root, strings for keys and ints for indices
}

// set replaces the matched value in its parent container.
func (m pathMatch) set(value any) bool {
	switch parent := m.parent.(type) {
	case map[string]any:
		parent[m.key] = value
		return true
	case []any:
		if m.index >= 0 && m.index < len(parent) {
			parent[m.index] = value
			return true
		}
	}
	return false
}

func parseJSONPath(expr string) (*jsonPath, error) {
	path := &jsonPath{raw: expr}
	input := strings.TrimSpace(expr)
	if input == "" {
		return nil, fmt.Errorf("empty json path")
	}
	if input[0] == '$' {
		input = input[1:]
	} else if input[0] != '.' && input[0] != '[' {
		// paths may omit the root, e.g. "Attributes.QueueArn"
		input = "." + input
	}

	pos := 0
	for pos < len(input) {
		recursive := false
		switch {
		case strings.HasPrefix(input[pos:], ".."):
			recursive = true
			pos += 2
		case input[pos] == '.':
			pos++
		case input[pos] == '[':
			// bracket selector directly attached to the previous segment
		default:
			return nil, fmt.Errorf("unexpected character %q at position %d in json path %q", input[pos], pos, expr)
		}

		if pos >= len(input) {
			return nil, fmt.Errorf("json path %q ends with a dangling selector", expr)
		}

		segment := pathSegment{recursive: recursive}
		switch {
		case input[pos] == '[':
			end := strings.IndexByte(input[pos:], ']')
			if end < 0 {
				return nil, fmt.Errorf("unbalanced '[' in json path %q", expr)
			}
			inner := strings.TrimSpace(input[pos+1 : pos+end])
			pos += end + 1

			switch {
			case inner == "*":
				segment.kind = selectorWildcard
			case isQuoted(inner):
				segment.kind = selectorName
				segment.name = inner[1 : len(inner)-1]
				segment.quoted = true
			default:
				idx, err := strconv.Atoi(inner)
				if err != nil {
					return nil, fmt.Errorf("unsupported selector [%s] in json path %q", inner, expr)
				}
				segment.kind = selectorIndex
				segment.index = idx
			}

		case input[pos] == '*':
			segment.kind = selectorWildcard
			pos++

		case input[pos] == '\'' || input[pos] == '"':
			quote := input[pos]
			end := strings.IndexByte(input[pos+1:], quote)
			if end < 0 {
				return nil, fmt.Errorf("unbalanced quote in json path %q", expr)
			}
			segment.kind = selectorName
			segment.name = input[pos+1 : pos+1+end]
			segment.quoted = true
			pos += end + 2

		default:
			end := pos
			for end < len(input) && input[end] != '.' && input[end] != '[' {
				end++
			}
			if end == pos {
				return nil, fmt.Errorf("empty selector in json path %q", expr)
			}
			segment.kind = selectorName
			segment.name = input[pos:end]
			pos = end
		}

		path.segments = append(path.segments, segment)
	}

	if len(path.segments) == 0 {
		return nil, fmt.Errorf("json path %q selects nothing", expr)
	}
	return path, nil
}

func isQuoted(s string) bool {
	if len(s) < 2 {
		return false
	}
	return (s[0] == '\'' && s[len(s)-1] == '\'') || (s[0] == '"' && s[len(s)-1] == '"')
}

// find returns all nodes matching the path. Maps are traversed in sorted key order so that results
// are deterministic, which matters for numbered reference replacements.
func (p *jsonPath) find(root any) []pathMatch {
	current := []pathMatch{{value: root}}

	for _, segment := range p.segments {
		candidates := current
		if segment.recursive {
			var expanded []pathMatch
			for _, candidate := range candidates {
				expanded = append(expanded, descendants(candidate)...)
			}
			candidates = expanded
		}

		var next []pathMatch
		for _, candidate := range candidates {
			next = append(next, selectFrom(candidate, segment)...)
		}
		current = next
		if len(current) == 0 {
			return nil
		}
	}

	return current
}

func selectFrom(node pathMatch, segment pathSegment) []pathMatch {
	switch container := node.value.(type) {
	case map[string]any:
		switch segment.kind {
		case selectorWildcard:
			matches := make([]pathMatch, 0, len(container))
			for _, key := range sortedKeys(container) {
				matches = append(matches, childOfMap(node, container, key))
			}
			return matches
		case selectorName:
			if _, ok := container[segment.name]; ok {
				return []pathMatch{childOfMap(node, container, segment.name)}
			}
		case selectorIndex:
			// A numeric selector can still address a map key such as "0".
			key := strconv.Itoa(segment.index)
			if _, ok := container[key]; ok {
				return []pathMatch{childOfMap(node, container, key)}
			}
		}

	case []any:
		switch segment.kind {
		case selectorWildcard:
			matches := make([]pathMatch, 0, len(container))
			for i := range container {
				matches = append(matches, childOfList(node, container, i))
			}
			return matches
		case selectorIndex:
			if idx, ok := listIndex(container, segment.index); ok {
				return []pathMatch{childOfList(node, container, idx)}
			}
		case selectorName:
			if segment.quoted {
				return nil
			}
			if parsed, err := strconv.Atoi(segment.name); err == nil {
				if idx, ok := listIndex(container, parsed); ok {
					return []pathMatch{childOfList(node, container, idx)}
				}
			}
		}
	}
	return nil
}

func listIndex(list []any, index int) (int, bool) {
	if index < 0 {
		index += len(list)
	}
	if index < 0 || index >= len(list) {
		return 0, false
	}
	return index, true
}

func childOfMap(parent pathMatch, container map[string]any, key string) pathMatch {
	return pathMatch{
		parent: container,
		key:    key,
		value:  container[key],
		path:   appendPath(parent.path, key),
	}
}

func childOfList(parent pathMatch, container []any, index int) pathMatch {
	return pathMatch{
		parent: container,
		index:  index,
		value:  container[index],
		path:   appendPath(parent.path, index),
	}
}

func appendPath(path []any, elem any) []any {
	out := make([]any, len(path), len(path)+1)
	copy(out, path)
	return append(out, elem)
}

// descendants returns the node itself followed by all nested nodes, depth first.
func descendants(node pathMatch) []pathMatch {
	out := []pathMatch{node}
	switch container := node.value.(type) {
	case map[string]any:
		for _, key := range sortedKeys(container) {
			out = append(out, descendants(childOfMap(node, container, key))...)
		}
	case []any:
		for i := range container {
			out = append(out, descendants(childOfList(node, container, i))...)
		}
	}
	return out
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
