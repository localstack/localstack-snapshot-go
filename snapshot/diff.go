package snapshot

import (
	"fmt"
	"sort"
	"strings"
)

// ChangeType describes how a snapshot value differs from the recorded one.
type ChangeType int

const (
	// ValuesChanged means both sides hold a value of the same type, but they differ.
	ValuesChanged ChangeType = iota
	// TypeChanged means both sides hold a value of a different type.
	TypeChanged
	// MapItemRemoved means the recorded snapshot has a key the observed state does not have.
	MapItemRemoved
	// MapItemAdded means the observed state has a key the recorded snapshot does not have.
	MapItemAdded
	// ListItemRemoved means the recorded snapshot has a list item the observed state does not have.
	ListItemRemoved
	// ListItemAdded means the observed state has a list item the recorded snapshot does not have.
	ListItemAdded
)

func (t ChangeType) String() string {
	switch t {
	case ValuesChanged:
		return "values_changed"
	case TypeChanged:
		return "type_changes"
	case MapItemRemoved:
		return "dictionary_item_removed"
	case MapItemAdded:
		return "dictionary_item_added"
	case ListItemRemoved:
		return "iterable_item_removed"
	case ListItemAdded:
		return "iterable_item_added"
	}
	return "unknown"
}

// Change is a single difference between the recorded and the observed state.
type Change struct {
	Type ChangeType
	// Path contains the keys (string) and list indices (int) leading to the change.
	Path []any
	// Expected is the recorded value, Actual the observed one. Either can be nil for added/removed.
	Expected any
	Actual   any
}

// String renders the change as `/path/to/value`.
func (c Change) String() string {
	return renderPath(c.Path)
}

// Diff compares a recorded snapshot value against an observed one and returns all differences.
func Diff(expected, actual any) []Change {
	var changes []Change
	diffValue(expected, actual, nil, &changes)
	sortChanges(changes)
	return changes
}

func diffValue(expected, actual any, path []any, changes *[]Change) {
	if expectedMap, ok := asMap(expected); ok {
		actualMap, ok := asMap(actual)
		if !ok {
			*changes = append(*changes, Change{Type: TypeChanged, Path: path, Expected: expected, Actual: actual})
			return
		}
		diffMap(expectedMap, actualMap, path, changes)
		return
	}

	if expectedList, ok := asList(expected); ok {
		actualList, ok := asList(actual)
		if !ok {
			*changes = append(*changes, Change{Type: TypeChanged, Path: path, Expected: expected, Actual: actual})
			return
		}
		diffList(expectedList, actualList, path, changes)
		return
	}

	if _, ok := asMap(actual); ok {
		*changes = append(*changes, Change{Type: TypeChanged, Path: path, Expected: expected, Actual: actual})
		return
	}
	if _, ok := asList(actual); ok {
		*changes = append(*changes, Change{Type: TypeChanged, Path: path, Expected: expected, Actual: actual})
		return
	}

	if expected == actual {
		return
	}
	changeType := ValuesChanged
	if !sameScalarType(expected, actual) {
		changeType = TypeChanged
	}
	*changes = append(*changes, Change{Type: changeType, Path: path, Expected: expected, Actual: actual})
}

func diffMap(expected, actual map[string]any, path []any, changes *[]Change) {
	for _, key := range sortedKeys(expected) {
		expectedValue := expected[key]
		actualValue, ok := actual[key]
		if !ok {
			*changes = append(*changes, Change{Type: MapItemRemoved, Path: appendPath(path, key), Expected: expectedValue})
			continue
		}
		diffValue(expectedValue, actualValue, appendPath(path, key), changes)
	}
	for _, key := range sortedKeys(actual) {
		if _, ok := expected[key]; !ok {
			*changes = append(*changes, Change{Type: MapItemAdded, Path: appendPath(path, key), Actual: actual[key]})
		}
	}
}

func diffList(expected, actual []any, path []any, changes *[]Change) {
	common := len(expected)
	if len(actual) < common {
		common = len(actual)
	}
	for i := 0; i < common; i++ {
		diffValue(expected[i], actual[i], appendPath(path, i), changes)
	}
	for i := common; i < len(expected); i++ {
		*changes = append(*changes, Change{Type: ListItemRemoved, Path: appendPath(path, i), Expected: expected[i]})
	}
	for i := common; i < len(actual); i++ {
		*changes = append(*changes, Change{Type: ListItemAdded, Path: appendPath(path, i), Actual: actual[i]})
	}
}

// sameScalarType reports whether two scalars are of the same JSON type.
func sameScalarType(a, b any) bool {
	return jsonTypeName(a) == jsonTypeName(b)
}

func jsonTypeName(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case bool:
		return "bool"
	case string:
		return "string"
	case map[string]any:
		return "object"
	case []any:
		return "list"
	default:
		return "number"
	}
}

// sortChanges orders changes by their path, so that reports are stable.
func sortChanges(changes []Change) {
	sort.SliceStable(changes, func(i, j int) bool {
		return comparePaths(changes[i].Path, changes[j].Path) < 0
	})
}

func comparePaths(a, b []any) int {
	for i := 0; i < len(a) && i < len(b); i++ {
		left, right := pathElementKey(a[i]), pathElementKey(b[i])
		if left != right {
			if left < right {
				return -1
			}
			return 1
		}
	}
	switch {
	case len(a) < len(b):
		return -1
	case len(a) > len(b):
		return 1
	}
	return 0
}

// pathElementKey renders a path element so that list indices sort numerically.
func pathElementKey(elem any) string {
	if index, ok := elem.(int); ok {
		return fmt.Sprintf("\x00%010d", index)
	}
	return fmt.Sprint(elem)
}

func renderPath(path []any) string {
	var builder strings.Builder
	for _, elem := range path {
		if index, ok := elem.(int); ok {
			// wrap iterable indices in [] to more clearly denote them being such
			builder.WriteString(fmt.Sprintf("/[%d]", index))
			continue
		}
		builder.WriteString("/")
		builder.WriteString(fmt.Sprint(elem))
	}
	if builder.Len() == 0 {
		return "/"
	}
	return builder.String()
}
