package snapshot

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestFormatJSONPath(t *testing.T) {
	cases := []struct {
		path []any
		want string
	}{
		{[]any{"Records", 1}, `"$..Records"`},
		{[]any{"Records", 1, 1, 1}, `"$..Records"`},
		{[]any{"Records", 1, "SomeKey"}, `"$..Records..SomeKey"`},
		{[]any{"Records", 1, 1, "SomeKey"}, `"$..Records..SomeKey"`},
		{[]any{"Records", 1, 1, 0, "SomeKey"}, `"$..Records..SomeKey"`},
		{[]any{"Records", "SomeKey"}, `"$..Records.SomeKey"`},
		{nil, `"$.."`},
		{[]any{1, 1, 0, "SomeKey"}, `"$..SomeKey"`},
		{[]any{"Some:Key"}, `"$..'Some:Key'"`},
		{[]any{"Some.Key"}, `"$..'Some.Key'"`},
		{[]any{"Some-Key"}, `"$..Some-Key"`},
		{[]any{"Some0Key"}, `"$..Some0Key"`},
	}

	for _, testCase := range cases {
		if got := formatJSONPath(testCase.path); got != testCase.want {
			t.Errorf("formatJSONPath(%v) = %s, want %s", testCase.path, got, testCase.want)
		}
	}
}

func TestDiffTypes(t *testing.T) {
	expected := map[string]any{
		"same":    "value",
		"changed": "before",
		"removed": "gone",
		"typed":   "1",
		"nested":  map[string]any{"list": []any{"a", "b", "c"}},
	}
	actual := map[string]any{
		"same":    "value",
		"changed": "after",
		"added":   "new",
		"typed":   json.Number("1"),
		"nested":  map[string]any{"list": []any{"a", "z"}},
	}

	changes := Diff(Normalize(expected), Normalize(actual))

	got := map[string]ChangeType{}
	for _, change := range changes {
		got[renderPath(change.Path)] = change.Type
	}

	want := map[string]ChangeType{
		"/added":           MapItemAdded,
		"/changed":         ValuesChanged,
		"/nested/list/[1]": ValuesChanged,
		"/nested/list/[2]": ListItemRemoved,
		"/removed":         MapItemRemoved,
		"/typed":           TypeChanged,
	}
	if len(got) != len(want) {
		t.Fatalf("expected %d changes, got %d: %v", len(want), len(got), got)
	}
	for path, changeType := range want {
		if got[path] != changeType {
			t.Errorf("change at %s = %s, want %s", path, got[path], changeType)
		}
	}
}

func TestDiffIsStableAcrossRuns(t *testing.T) {
	expected := Normalize(map[string]any{"z": 1, "a": 2, "m": 3})
	actual := Normalize(map[string]any{"z": 9, "a": 8, "m": 7})

	first := renderReport(MatchResult{Key: "key", Changes: Diff(expected, actual)}, false)
	for i := 0; i < 20; i++ {
		if got := renderReport(MatchResult{Key: "key", Changes: Diff(expected, actual)}, false); got != first {
			t.Fatalf("report is not stable:\n%s\nvs\n%s", first, got)
		}
	}
	// the diff lines are sorted by path
	if !strings.Contains(first, "/a 2 → 8") || strings.Index(first, "/a ") > strings.Index(first, "/m ") {
		t.Fatalf("unexpected report:\n%s", first)
	}
}

func TestRenderReport(t *testing.T) {
	expected := Normalize(map[string]any{
		"Records": []any{map[string]any{"Body": "expected", "Removed": "gone"}},
	})
	actual := Normalize(map[string]any{
		"Records": []any{map[string]any{"Body": "actual", "Added": "new"}},
	})

	report := renderReport(MatchResult{Key: "receive-message", Changes: Diff(expected, actual)}, false)

	for _, want := range []string{
		">> match key: receive-message",
		`(~) /Records/[0]/Body "expected" → "actual" ... (expected → actual)`,
		`(+) /Records/[0]/Added ( "new" )`,
		`(-) /Records/[0]/Removed ( "gone" )`,
		`["$..Records..Added", "$..Records..Body", "$..Records..Removed"]`,
	} {
		if !strings.Contains(report, want) {
			t.Errorf("expected the report to contain %q, got:\n%s", want, report)
		}
	}
}

func TestRenderReportColors(t *testing.T) {
	report := renderReport(MatchResult{
		Key:     "key",
		Changes: Diff(Normalize(map[string]any{"a": "b"}), Normalize(map[string]any{"a": "c"})),
	}, true)

	if !strings.Contains(report, "\x1b[33m(~)\x1b[0m") {
		t.Fatalf("expected a colorized report, got:\n%q", report)
	}
}
