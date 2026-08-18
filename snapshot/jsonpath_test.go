package snapshot

import (
	"testing"
)

func TestParseJSONPathErrors(t *testing.T) {
	for _, expr := range []string{"", "$.", "$..", "$[", "$[abc]", "$.a[1", "$..'unbalanced"} {
		if _, err := parseJSONPath(expr); err == nil {
			t.Errorf("expected %q to be rejected", expr)
		}
	}
}

func TestJSONPathFind(t *testing.T) {
	state := map[string]any{
		"a": map[string]any{
			"b": []any{
				map[string]any{"c": "first"},
				map[string]any{"c": "second"},
			},
			"d": "value-d",
		},
		"e":     map[string]any{"b": []any{"nested-b"}},
		"a.dot": "quoted",
	}

	cases := []struct {
		expr string
		want []string
	}{
		{"$.a.d", []string{"value-d"}},
		{"a.d", []string{"value-d"}},
		{"$..d", []string{"value-d"}},
		{"$..c", []string{"first", "second"}},
		{"$.a.b[0].c", []string{"first"}},
		{"$.a.b.1.c", []string{"second"}},
		{"$.a.b[-1].c", []string{"second"}},
		{"$.a.b[*].c", []string{"first", "second"}},
		{"$..b[0]", []string{`{"c":"first"}`, "nested-b"}},
		{"$.'a.dot'", []string{"quoted"}},
		{"$['a.dot']", []string{"quoted"}},
		{`$["a.dot"]`, []string{"quoted"}},
		{"$.a.missing", nil},
		{"$.a.b[7]", nil},
	}

	for _, testCase := range cases {
		t.Run(testCase.expr, func(t *testing.T) {
			path, err := parseJSONPath(testCase.expr)
			if err != nil {
				t.Fatalf("could not parse %q: %v", testCase.expr, err)
			}

			matches := path.find(state)
			if len(matches) != len(testCase.want) {
				t.Fatalf("expected %d matches for %q, got %d", len(testCase.want), testCase.expr, len(matches))
			}
			for i, match := range matches {
				if got := canonicalString(match.value); got != testCase.want[i] {
					t.Errorf("match %d for %q = %s, want %s", i, testCase.expr, got, testCase.want[i])
				}
			}
		})
	}
}

func TestJSONPathSet(t *testing.T) {
	state := map[string]any{"a": map[string]any{"b": []any{"one", "two"}}}

	path, err := parseJSONPath("$..b[1]")
	if err != nil {
		t.Fatalf("could not parse the path: %v", err)
	}
	matches := path.find(state)
	if len(matches) != 1 {
		t.Fatalf("expected one match, got %d", len(matches))
	}
	if !matches[0].set("replaced") {
		t.Fatal("expected the match to be replaceable")
	}

	requireJSONEqual(t, map[string]any{"a": map[string]any{"b": []any{"one", "replaced"}}}, state)
}

func TestJSONPathFindIsDeterministic(t *testing.T) {
	state := map[string]any{
		"z": map[string]any{"id": "z-id"},
		"a": map[string]any{"id": "a-id"},
		"m": map[string]any{"id": "m-id"},
	}

	path, err := parseJSONPath("$..id")
	if err != nil {
		t.Fatalf("could not parse the path: %v", err)
	}

	// maps are traversed in sorted key order, which keeps numbered reference replacements stable
	for i := 0; i < 20; i++ {
		matches := path.find(state)
		if len(matches) != 3 {
			t.Fatalf("expected 3 matches, got %d", len(matches))
		}
		for i, want := range []string{"a-id", "m-id", "z-id"} {
			if matches[i].value != want {
				t.Fatalf("match %d = %v, want %s", i, matches[i].value, want)
			}
		}
	}
}
