package snapshot

import (
	"bytes"
	"regexp"
	"strings"
	"testing"
)

func TestSimpleDiffNoChange(t *testing.T) {
	snap, ft := newSession(t, map[string]any{"key_a": map[string]any{"a": 3}})
	snap.Match("key_a", map[string]any{"a": 3})
	ft.runCleanups()

	requireMatched(t, snap, ft)
}

func TestSimpleDiffChange(t *testing.T) {
	snap, ft := newSession(t, map[string]any{"key_a": map[string]any{"a": 3}})
	snap.Match("key_a", map[string]any{"a": 5})
	ft.runCleanups()

	requireFailure(t, ft, "parity snapshot failed")
	requireFailure(t, ft, "/a 3 → 5")
}

func TestDiffWithReader(t *testing.T) {
	snap, ft := newSession(t, map[string]any{"key_a": map[string]any{"a": "data"}})
	snap.Match("key_a", map[string]any{"a": bytes.NewReader([]byte("data"))})
	ft.runCleanups()

	requireMatched(t, snap, ft)
}

func TestMultipleMatchWithSameKeyFails(t *testing.T) {
	snap, ft := newSession(t, map[string]any{"key_a": map[string]any{"a": 3}})
	snap.Match("key_a", map[string]any{"a": 3})
	snap.Match("key_a", map[string]any{"a": 3})

	requireFailure(t, ft, "used multiple times in the same test scope")
}

func TestMissingRecordedStateForKey(t *testing.T) {
	snap, ft := newSession(t, map[string]any{"key_a": map[string]any{"a": 3}})
	snap.Match("key_b", map[string]any{"a": 3})

	requireFailure(t, ft, `no state for "key_b" recorded`)
}

func TestContextReplacement(t *testing.T) {
	snap, ft := newSession(t, map[string]any{
		"key_a": map[string]any{"aaa": "<A:1>", "bbb": "<A:1> hello"},
	})
	snap.AddTransformer(Transform.KeyValue("aaa", "A", true))
	snap.Match("key_a", map[string]any{"aaa": "something", "bbb": "something hello"})
	ft.runCleanups()

	requireMatched(t, snap, ft)
}

func TestReplacementKeyValueSubstring(t *testing.T) {
	snap, ft := newSession(t, map[string]any{
		"key_a": map[string]any{
			"aaa": "hellA",
			"aab": "this is a test",
			"b":   map[string]any{"aaa": "another teA"},
		},
	})
	// only the last two characters of the value are replaced
	snap.AddTransformer(Transform.KeyValueMatch(func(key string, value any) (any, bool) {
		str, ok := value.(string)
		if key != "aaa" || !ok || len(str) < 2 {
			return nil, false
		}
		return str[len(str)-2:], true
	}, "A", false))

	snap.Match("key_a", map[string]any{
		"aaa": "helloo",
		"aab": "this is a test",
		"b":   map[string]any{"aaa": "another test"},
	})
	ft.runCleanups()

	requireMatched(t, snap, ft)
}

func TestStructIsNormalized(t *testing.T) {
	type nested struct {
		Name string
	}
	type payload struct {
		Name     string `json:"name"`
		Nested   *nested
		Listed   []any
		Skipped  string `json:"-"`
		Empty    string `json:"empty,omitempty"`
		internal string
	}

	snap, ft := newSession(t, map[string]any{
		"key_a": map[string]any{
			"name":   "myname",
			"Nested": map[string]any{"Name": "nestedmyname"},
			"Listed": []any{map[string]any{"Name": "listedmyname"}, "otherobj"},
		},
	})
	snap.Match("key_a", payload{
		Name:     "myname",
		Nested:   &nested{Name: "nestedmyname"},
		Listed:   []any{nested{Name: "listedmyname"}, "otherobj"},
		Skipped:  "n/a",
		internal: "n/a",
	})
	ft.runCleanups()

	requireMatched(t, snap, ft)
}

func TestStructChangeIsReported(t *testing.T) {
	type payload struct {
		Name string `json:"name"`
	}

	snap, ft := newSession(t, map[string]any{"key_a": map[string]any{"name": "myname"}})
	snap.Match("key_a", payload{Name: "diffname"})
	ft.runCleanups()

	requireFailure(t, ft, "parity snapshot failed")
}

func TestNonHomogeneousList(t *testing.T) {
	snap, ft := newSession(t, map[string]any{
		"key1": []any{map[string]any{"key2": "value1"}, "value2", 3},
	})
	snap.Match("key1", []any{map[string]any{"key2": "value1"}, "value2", 3})
	ft.runCleanups()

	requireMatched(t, snap, ft)
}

func TestDotInSkipVerificationPath(t *testing.T) {
	recorded := map[string]any{
		"key_a": map[string]any{
			"aaa": "hello",
			"aab": "this is a test",
			"b":   map[string]any{"a.aa": "another test"},
		},
	}
	observed := map[string]any{
		"aaa": "hello",
		"aab": "this is a test-fail",
		"b":   map[string]any{"a.aa": "another test-fail"},
	}

	t.Run("without skipping", func(t *testing.T) {
		snap, ft := newSession(t, recorded)
		snap.Match("key_a", observed)
		ft.runCleanups()
		requireFailure(t, ft, "parity snapshot failed")
	})

	t.Run("unescaped path", func(t *testing.T) {
		snap, ft := newSession(t, recorded)
		snap.SkipVerifyPaths("$..aab", "$..b.a.aa")
		snap.Match("key_a", observed)
		ft.runCleanups()
		requireFailure(t, ft, "parity snapshot failed")
	})

	t.Run("escaped path", func(t *testing.T) {
		snap, ft := newSession(t, recorded)
		snap.SkipVerifyPaths("$..aab", "$..b.'a.aa'")
		snap.Match("key_a", observed)
		ft.runCleanups()
		requireMatched(t, snap, ft)
	})
}

func TestListAsLastNodeInSkipVerificationPath(t *testing.T) {
	recorded := map[string]any{"key_a": map[string]any{"aaa": []any{"item1", "item2", "item3"}}}
	observed := map[string]any{"aaa": []any{"item1", "different-value"}}

	t.Run("without skipping", func(t *testing.T) {
		snap, ft := newSession(t, recorded)
		snap.Match("key_a", observed)
		ft.runCleanups()
		requireFailure(t, ft, "parity snapshot failed")
	})

	for _, paths := range [][]string{
		{"$..aaa[1]", "$..aaa[2]"},
		{"$..aaa.1", "$..aaa.2"},
	} {
		t.Run(strings.Join(paths, ","), func(t *testing.T) {
			snap, ft := newSession(t, recorded)
			snap.SkipVerifyPaths(paths...)
			snap.Match("key_a", observed)
			ft.runCleanups()
			requireMatched(t, snap, ft)
		})
	}
}

func TestListAsLastNodeInSkipVerificationPathComplex(t *testing.T) {
	recorded := map[string]any{
		"key_a": map[string]any{
			"aaa": []any{
				map[string]any{"aab": []any{"aac", "aad"}},
				map[string]any{"aab": []any{"aac", "aad"}},
				map[string]any{"aab": []any{"aac", "aad"}},
			},
		},
	}
	observed := map[string]any{
		"aaa": []any{
			map[string]any{"aab": []any{"aac", "bad-value"}, "bbb": "value"},
			map[string]any{"aab": []any{"aac", "aad", "bad-value"}},
			map[string]any{"aab": []any{"bad-value", "aad"}},
		},
	}

	t.Run("without skipping", func(t *testing.T) {
		snap, ft := newSession(t, recorded)
		snap.Match("key_a", observed)
		ft.runCleanups()
		requireFailure(t, ft, "parity snapshot failed")
	})

	for _, paths := range [][]string{
		{"$..aaa[0].aab[1]", "$..aaa[0].bbb", "$..aaa[1].aab[2]", "$..aaa[2].aab[0]"},
		{"$..aaa.0..aab.1", "$..aaa.0..bbb", "$..aaa.1..aab.2", "$..aaa.2..aab.0"},
	} {
		t.Run(strings.Join(paths, ","), func(t *testing.T) {
			snap, ft := newSession(t, recorded)
			snap.SkipVerifyPaths(paths...)
			snap.Match("key_a", observed)
			ft.runCleanups()
			requireMatched(t, snap, ft)
		})
	}
}

func TestListAsMidNodeInSkipVerificationPath(t *testing.T) {
	recorded := map[string]any{
		"key_a": map[string]any{"aaa": []any{
			map[string]any{"aab": "value1"},
			map[string]any{"aab": "value2"},
		}},
	}
	observed := map[string]any{"aaa": []any{
		map[string]any{"aab": "value1"},
		map[string]any{"aab": "bad-value"},
	}}

	t.Run("without skipping", func(t *testing.T) {
		snap, ft := newSession(t, recorded)
		snap.Match("key_a", observed)
		ft.runCleanups()
		requireFailure(t, ft, "parity snapshot failed")
	})

	for _, path := range []string{"$..aaa[1].aab", "$..aaa.1.aab"} {
		t.Run(path, func(t *testing.T) {
			snap, ft := newSession(t, recorded)
			snap.SkipVerifyPaths(path)
			snap.Match("key_a", observed)
			ft.runCleanups()
			requireMatched(t, snap, ft)
		})
	}
}

func TestListAsLastNodeInSkipVerificationPathNested(t *testing.T) {
	recorded := map[string]any{
		"key_a": map[string]any{"aaa": []any{
			"bbb", "ccc", []any{"ddd", "eee", []any{"fff", "ggg"}},
		}},
	}
	observed := map[string]any{"aaa": []any{
		"bbb", "ccc", []any{"bad-value", "eee", []any{"fff", "ggg"}},
	}}

	t.Run("without skipping", func(t *testing.T) {
		snap, ft := newSession(t, recorded)
		snap.Match("key_a", observed)
		ft.runCleanups()
		requireFailure(t, ft, "parity snapshot failed")
	})

	// the last two skip almost everything, as they match the first element of every list inside `aaa`
	for _, path := range []string{"$..aaa[2][0]", "$..aaa.2[0]", "$..aaa..[0]", "$..aaa..0"} {
		t.Run(path, func(t *testing.T) {
			snap, ft := newSession(t, recorded)
			snap.SkipVerifyPaths(path)
			snap.Match("key_a", observed)
			ft.runCleanups()
			requireMatched(t, snap, ft)
		})
	}
}

func TestSkipVerifyDisablesVerification(t *testing.T) {
	snap, ft := newSession(t, map[string]any{"key_a": map[string]any{"a": 3}})
	snap.SkipVerify()
	snap.Match("key_a", map[string]any{"a": 5})
	ft.runCleanups()

	requireNoFailure(t, ft)
	if len(snap.Results()) != 0 {
		t.Fatalf("expected no keys to be compared, got %d", len(snap.Results()))
	}
}

func TestSessionWithoutMatchIsNoSnapshotTest(t *testing.T) {
	_, ft := newSession(t, nil)
	ft.runCleanups()

	requireNoFailure(t, ft)
}

func TestLegacyAPI(t *testing.T) {
	snap, ft := newSession(t, map[string]any{
		"key_a": map[string]any{
			"replaced":  "<value>",
			"skipped":   "<skipped>",
			"unrelated": "keep-me",
		},
	})
	snap.RegisterReplacement(regexp.MustCompile(`repl[a-z]+ment`), "<value>")
	snap.SkipKey(regexp.MustCompile(`skip`), "<skipped>")
	snap.Match("key_a", map[string]any{
		"replaced":  "replacement",
		"skipped":   "some-random-id",
		"unrelated": "keep-me",
	})
	ft.runCleanups()

	requireMatched(t, snap, ft)
}

func TestTransformerPriority(t *testing.T) {
	snap, ft := newSession(t, map[string]any{"key_a": map[string]any{"a": "second"}})
	// the higher priority transformer runs last, so it wins
	snap.AddTransformerWithPriority(Transform.Text("first", "second"), 10)
	snap.AddTransformerWithPriority(Transform.Text("original", "first"), -10)
	snap.Match("key_a", map[string]any{"a": "original"})
	ft.runCleanups()

	requireMatched(t, snap, ft)
}

func TestReferenceReplacementOfNonStringFails(t *testing.T) {
	snap, ft := newSession(t, map[string]any{"key_a": map[string]any{"a": 3}})
	snap.AddTransformer(Transform.KeyValue("a", "number", true))
	snap.Match("key_a", map[string]any{"a": 3})
	ft.runCleanups()

	requireFailure(t, ft, "is not a string")
}

func TestEmbeddedJSONStringIsParsed(t *testing.T) {
	snap, ft := newSession(t, map[string]any{
		"key_a": map[string]any{"Policy": map[string]any{"Version": "2012-10-17"}},
	})
	snap.Match("key_a", map[string]any{"Policy": `{"Version": "2012-10-17"}`})
	ft.runCleanups()

	requireMatched(t, snap, ft)
}
