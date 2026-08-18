package snapshot

import (
	"fmt"
	"testing"
)

// applyTransformer runs a transformer and applies the replacements it registered on the serialized
// state, mirroring what a session does.
func applyTransformer(t *testing.T, transformer Transformer, input map[string]any) (map[string]any, *TransformContext) {
	t.Helper()

	state, err := normalizeMap(input)
	if err != nil {
		t.Fatalf("could not normalize the input: %v", err)
	}

	ctx := NewTransformContext()
	transformed := transformer.Transform(state, ctx)

	serialized, err := marshalValue(transformed)
	if err != nil {
		t.Fatalf("could not serialize the transformed state: %v", err)
	}
	for _, replace := range ctx.SerializedReplacements() {
		serialized = replace(serialized)
	}

	parsed, err := unmarshalValue(serialized)
	if err != nil {
		t.Fatalf("could not parse the transformed state: %v", err)
	}
	result, ok := asMap(parsed)
	if !ok {
		t.Fatalf("expected the transformed state to be an object, got %T", parsed)
	}
	return result, ctx
}

func requireJSONEqual(t *testing.T, want, got any) {
	t.Helper()

	wantJSON, err := marshalValue(Normalize(want))
	if err != nil {
		t.Fatalf("could not serialize the expected value: %v", err)
	}
	gotJSON, err := marshalValue(Normalize(got))
	if err != nil {
		t.Fatalf("could not serialize the actual value: %v", err)
	}
	if wantJSON != gotJSON {
		t.Fatalf("unexpected result\nexpected: %s\nactual:   %s", wantJSON, gotJSON)
	}
}

func TestKeyValueReplacement(t *testing.T) {
	input := func() map[string]any {
		return map[string]any{
			"hello":  "world",
			"hello2": "again",
			"path":   map[string]any{"to": map[string]any{"anotherkey": "hi", "inside": map[string]any{"hello": "inside"}}},
		}
	}

	t.Run("without reference replacement", func(t *testing.T) {
		result, ctx := applyTransformer(t, Transform.KeyValue("hello", "placeholder", false), input())

		requireJSONEqual(t, map[string]any{
			"hello":  "placeholder",
			"hello2": "again",
			"path":   map[string]any{"to": map[string]any{"anotherkey": "hi", "inside": map[string]any{"hello": "placeholder"}}},
		}, result)
		if len(ctx.SerializedReplacements()) != 0 {
			t.Fatalf("expected no serialized replacements, got %d", len(ctx.SerializedReplacements()))
		}
	})

	t.Run("with reference replacement", func(t *testing.T) {
		result, ctx := applyTransformer(t, Transform.KeyValue("hello", "placeholder", true), input())

		requireJSONEqual(t, map[string]any{
			"hello":  "<placeholder:1>",
			"hello2": "again",
			"path": map[string]any{"to": map[string]any{
				"anotherkey":      "hi",
				"<placeholder:2>": map[string]any{"hello": "<placeholder:2>"},
			}},
		}, result)
		if len(ctx.SerializedReplacements()) != 2 {
			t.Fatalf("expected 2 serialized replacements, got %d", len(ctx.SerializedReplacements()))
		}
	})
}

func TestKeyValueReplacementCustomFunction(t *testing.T) {
	input := func() map[string]any {
		return map[string]any{
			"hello":  "12characters",
			"hello2": "again",
			"path": map[string]any{"to": map[string]any{
				"anotherkey":      "hi",
				"twelvesymbol":    map[string]any{"hello": "twelvesymbol"},
				"fifteen_symbols": map[string]any{"hello": "fifteen_symbols"},
			}},
		}
	}
	replacement := func(_ string, value any) string {
		return fmt.Sprintf("placeholder(%d)", len(value.(string)))
	}

	t.Run("without reference replacement", func(t *testing.T) {
		result, ctx := applyTransformer(t, Transform.KeyValueFunc("hello", replacement, false), input())

		requireJSONEqual(t, map[string]any{
			"hello":  "placeholder(12)",
			"hello2": "again",
			"path": map[string]any{"to": map[string]any{
				"anotherkey":      "hi",
				"twelvesymbol":    map[string]any{"hello": "placeholder(12)"},
				"fifteen_symbols": map[string]any{"hello": "placeholder(15)"},
			}},
		}, result)
		if len(ctx.SerializedReplacements()) != 0 {
			t.Fatalf("expected no serialized replacements, got %d", len(ctx.SerializedReplacements()))
		}
	})

	t.Run("with reference replacement", func(t *testing.T) {
		// the replacement counters are per replacement, so placeholder(15) starts from 1 again
		result, ctx := applyTransformer(t, Transform.KeyValueFunc("hello", replacement, true), input())

		requireJSONEqual(t, map[string]any{
			"hello":  "<placeholder(12):1>",
			"hello2": "again",
			"path": map[string]any{"to": map[string]any{
				"anotherkey":          "hi",
				"<placeholder(12):2>": map[string]any{"hello": "<placeholder(12):2>"},
				"<placeholder(15):1>": map[string]any{"hello": "<placeholder(15):1>"},
			}},
		}, result)
		if len(ctx.SerializedReplacements()) != 3 {
			t.Fatalf("expected 3 serialized replacements, got %d", len(ctx.SerializedReplacements()))
		}
	})
}

func TestKeyValueReplacementWithFalsyValue(t *testing.T) {
	result, _ := applyTransformer(t, Transform.KeyValue("somenumber", "placeholder", false), map[string]any{
		"hello":      "world",
		"somenumber": 0,
	})

	requireJSONEqual(t, map[string]any{"hello": "world", "somenumber": "placeholder"}, result)
}

func TestReplacementWithReference(t *testing.T) {
	input := func() map[string]any {
		return map[string]any{
			"also-me": "b",
			"path": map[string]any{
				"to":      map[string]any{"anotherkey": "hi", "test": map[string]any{"hello": "replaceme"}},
				"another": map[string]any{"key": "this/replaceme/hello"},
			},
			"b":    map[string]any{"a/b/replaceme.again": "bb"},
			"test": map[string]any{"inside": map[string]any{"path": map[string]any{"to": map[string]any{"test": map[string]any{"hello": "also-me"}}}}},
		}
	}
	expected := map[string]any{
		"<MYVALUE:2>": "b",
		"path": map[string]any{
			"to":      map[string]any{"anotherkey": "hi", "test": map[string]any{"hello": "<MYVALUE:1>"}},
			"another": map[string]any{"key": "this/<MYVALUE:1>/hello"},
		},
		"b":    map[string]any{"a/b/<MYVALUE:1>.again": "bb"},
		"test": map[string]any{"inside": map[string]any{"path": map[string]any{"to": map[string]any{"test": map[string]any{"hello": "<MYVALUE:2>"}}}}},
	}

	for name, transformer := range map[string]Transformer{
		"key_value": Transform.KeyValue("hello", "MYVALUE", true),
		"jsonpath":  Transform.JSONPath("$..path.to.test.hello", "MYVALUE", true),
	} {
		t.Run(name, func(t *testing.T) {
			result, ctx := applyTransformer(t, transformer, input())
			requireJSONEqual(t, expected, result)
			if len(ctx.SerializedReplacements()) != 2 {
				t.Fatalf("expected 2 serialized replacements, got %d", len(ctx.SerializedReplacements()))
			}
		})
	}
}

func TestJSONPathWithoutReferenceReplacement(t *testing.T) {
	result, _ := applyTransformer(t, Transform.JSONPath("$..Attributes.QueueArn", "<arn>", false), map[string]any{
		"Attributes": map[string]any{"QueueArn": "arn:aws:sqs:us-east-1:000000000000:my-queue", "Other": "keep"},
	})

	requireJSONEqual(t, map[string]any{
		"Attributes": map[string]any{"QueueArn": "<arn>", "Other": "keep"},
	}, result)
}

func TestRegexTransformer(t *testing.T) {
	result, _ := applyTransformer(t, Transform.Regex("hello", "new-value"), map[string]any{
		"hello":  "world",
		"hello2": "again",
		"path":   map[string]any{"to": map[string]any{"anotherkey": "hi", "inside": map[string]any{"hello": "inside"}}},
	})

	requireJSONEqual(t, map[string]any{
		"new-value":  "world",
		"new-value2": "again",
		"path":       map[string]any{"to": map[string]any{"anotherkey": "hi", "inside": map[string]any{"new-value": "inside"}}},
	}, result)
}

func TestTextTransformer(t *testing.T) {
	for _, value := range []string{
		"a+b",
		"question?",
		"amount: $4.00",
		"emoji: ^^",
		"sentence.",
		"others (like so)",
		"special {char}",
	} {
		t.Run(value, func(t *testing.T) {
			result, _ := applyTransformer(t, Transform.Text(value, "<value>"), map[string]any{
				"key": fmt.Sprintf("some %s with more text", value),
			})
			requireJSONEqual(t, map[string]any{"key": "some <value> with more text"}, result)
		})
	}
}

func TestNestedSortingTransformer(t *testing.T) {
	result, _ := applyTransformer(t, Transform.SortingByKey("subsegments", "name"), map[string]any{
		"subsegments": []any{
			map[string]any{
				"name": "mysubsegment",
				"subsegments": []any{
					map[string]any{"name": "b"},
					map[string]any{"name": "a"},
				},
			},
		},
	})

	requireJSONEqual(t, map[string]any{
		"subsegments": []any{
			map[string]any{
				"name": "mysubsegment",
				"subsegments": []any{
					map[string]any{"name": "a"},
					map[string]any{"name": "b"},
				},
			},
		},
	}, result)
}

func TestSortingTransformerDefaultLess(t *testing.T) {
	result, _ := applyTransformer(t, Transform.Sorting("names", nil), map[string]any{
		"names": []any{"c", "a", "b"},
	})

	requireJSONEqual(t, map[string]any{"names": []any{"a", "b", "c"}}, result)
}

func TestSortingTransformerOnNonList(t *testing.T) {
	ctx := NewTransformContext()
	Transform.Sorting("names", nil).Transform(map[string]any{"names": "not-a-list"}, ctx)

	if ctx.Err() == nil {
		t.Fatal("expected an error when sorting a non-list")
	}
}

func TestJSONStringTransformer(t *testing.T) {
	cases := []struct {
		name  string
		input any
		want  any
	}{
		{"simple_json_object", `{"a": "b"}`, map[string]any{"a": "b"}},
		{"formatted_json_object", "{\n  \"a\": \"b\"\n}", map[string]any{"a": "b"}},
		{"json_with_whitespaces", "\n  {\"a\": \"b\"}", map[string]any{"a": "b"}},
		{"malformed_json", `{"a": 42}malformed`, `{"a": 42}malformed`},
		{"simple_json_list", `["a", "b"]`, []any{"a", "b"}},
		{"nested_json_object", `{"a": "{\"b\":42}"}`, map[string]any{"a": map[string]any{"b": 42}}},
		{"nested_formatted_json_object_with_whitespaces", `{"a": "\n  {\n  \"b\":42}"}`, map[string]any{"a": map[string]any{"b": 42}}},
		{"nested_json_list", `{"a": "[{\"b\":\"c\"}]"}`, map[string]any{"a": []any{map[string]any{"b": "c"}}}},
		{"malformed_nested_json", `{"a": "{\"b\":42malformed}"}`, map[string]any{"a": `{"b":42malformed}`}},
		{"empty_list", `[]`, []any{}},
		{"empty_object", `{}`, map[string]any{}},
		{"empty_string", ``, ``},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			result, _ := applyTransformer(t, Transform.JSONString("key"), map[string]any{"key": testCase.input})
			requireJSONEqual(t, map[string]any{"key": testCase.want}, result)
		})
	}
}

func TestJSONStringTransformerInNestedKey(t *testing.T) {
	key := "nested-key-in-an-object-hidden-inside-a-list"
	result, _ := applyTransformer(t, Transform.JSONString(key), map[string]any{
		"top-level-key": []any{map[string]any{key: `{"a": "b"}`}},
	})

	requireJSONEqual(t, map[string]any{"top-level-key": []any{map[string]any{key: map[string]any{"a": "b"}}}}, result)
}

func TestTimestampTransformer(t *testing.T) {
	result, _ := applyTransformer(t, Transform.Timestamp(), map[string]any{
		"lambda": map[string]any{
			"FunctionName": "lambdafn",
			"LastModified": "2023-10-09T12:49:50.000+0000",
		},
		"cfn": map[string]any{
			"StackName":    "cfnstack",
			"CreationTime": "2023-11-20T18:39:36.014000+00:00",
		},
		"sfn": map[string]any{
			"name":         "statemachine",
			"creationDate": "2023-11-21T07:14:12.243000+01:00",
			"sfninternal":  "2023-11-21T07:14:12.243Z",
		},
	})

	requireJSONEqual(t, map[string]any{
		"lambda": map[string]any{
			"FunctionName": "lambdafn",
			"LastModified": "<timestamp:2022-07-13T13:48:01.000+0000>",
		},
		"cfn": map[string]any{
			"StackName":    "cfnstack",
			"CreationTime": "<timestamp:2022-07-13T13:48:01.000000+00:00>",
		},
		"sfn": map[string]any{
			"name":         "statemachine",
			"creationDate": "<timestamp:2022-07-13T13:48:01.000000+00:00>",
			"sfninternal":  "<timestamp:2022-07-13T13:48:01.000Z>",
		},
	}, result)
}

func TestTimestampTransformerHandlesGoTimes(t *testing.T) {
	result, _ := applyTransformer(t, Transform.Timestamp(), map[string]any{
		"millis":  "2023-10-09T12:49:50.123Z",
		"seconds": "2023-10-09T12:49:50Z",
		"nanos":   "2023-10-09T12:49:50.123456789Z",
		"offset":  "2023-10-09T12:49:50+02:00",
		"nope":    "not-a-timestamp",
	})

	requireJSONEqual(t, map[string]any{
		"millis":  "<timestamp:2022-07-13T13:48:01.000Z>",
		"seconds": "<timestamp:2022-07-13T13:48:01Z>",
		"nanos":   "<timestamp:2022-07-13T13:48:01Z>",
		"offset":  "<timestamp:2022-07-13T13:48:01Z>",
		"nope":    "not-a-timestamp",
	}, result)
}

func TestResponseMetadataTransformer(t *testing.T) {
	cases := []struct {
		name  string
		input map[string]any
		want  map[string]any
	}{
		{
			name:  "with headers",
			input: map[string]any{"ResponseMetadata": map[string]any{"HTTPHeaders": map[string]any{"header1": "value1"}}},
			want:  map[string]any{"ResponseMetadata": map[string]any{"HTTPHeaders": map[string]any{}}},
		},
		{
			name: "with headers and status code",
			input: map[string]any{"ResponseMetadata": map[string]any{
				"HTTPHeaders":    map[string]any{"header1": "value1"},
				"HTTPStatusCode": 500,
			}},
			want: map[string]any{"ResponseMetadata": map[string]any{
				"HTTPHeaders":    map[string]any{},
				"HTTPStatusCode": 500,
			}},
		},
		{
			name:  "with status code only",
			input: map[string]any{"ResponseMetadata": map[string]any{"HTTPStatusCode": 500, "RandomData": "random"}},
			want:  map[string]any{"ResponseMetadata": map[string]any{"HTTPStatusCode": 500, "RandomData": "random"}},
		},
		{
			name:  "with empty response metadata",
			input: map[string]any{"ResponseMetadata": map[string]any{"NotHeaders": "data"}},
			want:  map[string]any{"ResponseMetadata": map[string]any{"NotHeaders": "data"}},
		},
		{
			name:  "with headers of the wrong type",
			input: map[string]any{"ResponseMetadata": map[string]any{"HTTPHeaders": "data"}},
			want:  map[string]any{"ResponseMetadata": map[string]any{"HTTPHeaders": "data"}},
		},
		{
			name: "headers filtering",
			input: map[string]any{"ResponseMetadata": map[string]any{"HTTPHeaders": map[string]any{
				"content_type": "value1",
				"header1":      "value1",
			}}},
			want: map[string]any{"ResponseMetadata": map[string]any{"HTTPHeaders": map[string]any{
				"content_type": "value1",
			}}},
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			result, ctx := applyTransformer(t, Transform.ResponseMetadata(), testCase.input)
			requireJSONEqual(t, testCase.want, result)
			if len(ctx.SerializedReplacements()) != 0 {
				t.Fatalf("expected no serialized replacements, got %d", len(ctx.SerializedReplacements()))
			}
		})
	}
}

func TestGenericTransformer(t *testing.T) {
	result, _ := applyTransformer(t, Transform.Generic(func(input map[string]any, _ *TransformContext) map[string]any {
		input["added"] = "by-transformer"
		return input
	}), map[string]any{"key": "value"})

	requireJSONEqual(t, map[string]any{"key": "value", "added": "by-transformer"}, result)
}

func TestCamelToHyphen(t *testing.T) {
	for input, want := range map[string]string{
		"FunctionName": "function-name",
		"QueueUrl":     "queue-url",
		"hello":        "hello",
		"ARN":          "a-r-n",
	} {
		if got := camelToHyphen(input); got != want {
			t.Errorf("camelToHyphen(%q) = %q, want %q", input, got, want)
		}
	}
}
