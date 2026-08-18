package snapshot

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

type marshaler struct{}

func (marshaler) MarshalJSON() ([]byte, error) { return []byte(`{"custom":true}`), nil }

type stringKey string

func TestNormalizeScalars(t *testing.T) {
	cases := []struct {
		name  string
		input any
		want  string
	}{
		{"nil", nil, "null"},
		{"bool", true, "true"},
		{"string", "hello", `"hello"`},
		{"int", 42, "42"},
		{"int64", int64(-7), "-7"},
		{"uint", uint8(3), "3"},
		{"float whole", 3.0, "3"},
		{"float fraction", 1.5, "1.5"},
		{"large int64", int64(9007199254740993), "9007199254740993"},
		{"bytes", []byte("data"), `"data"`},
		{"json number", json.Number("1.10"), "1.10"},
		{"raw message", json.RawMessage(`{"a":1}`), `{"a":1}`},
		{"error", errors.New("boom"), `"boom"`},
		{"reader", bytes.NewBufferString("streamed"), `"streamed"`},
		{"marshaler", marshaler{}, `{"custom":true}`},
		{"time", time.Date(2023, 10, 9, 12, 49, 50, 123456789, time.UTC), `"2023-10-09T12:49:50.123Z"`},
		{"nil slice", []string(nil), "null"},
		{"empty slice", []string{}, "[]"},
		{"nil map", map[string]any(nil), "null"},
		{"typed map key", map[stringKey]int{"a": 1}, `{"a":1}`},
		{"int map key", map[int]string{2: "b"}, `{"2":"b"}`},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got, err := marshalValue(Normalize(testCase.input))
			if err != nil {
				t.Fatalf("could not serialize: %v", err)
			}
			if got != testCase.want {
				t.Fatalf("Normalize(%#v) = %s, want %s", testCase.input, got, testCase.want)
			}
		})
	}
}

func TestNormalizeStruct(t *testing.T) {
	type inner struct {
		Value string `json:"value"`
	}
	type embedded struct {
		Inherited string `json:"inherited"`
	}
	type outer struct {
		embedded
		Renamed  string  `json:"renamed"`
		Pointer  *inner  `json:"pointer"`
		NilPtr   *inner  `json:"nil_ptr"`
		Omitted  string  `json:"omitted,omitempty"`
		Ignored  string  `json:"-"`
		Number   float64 `json:"number"`
		Untagged bool
		private  string
	}

	got, err := marshalValue(Normalize(outer{
		embedded: embedded{Inherited: "from-embedded"},
		Renamed:  "renamed-value",
		Pointer:  &inner{Value: "nested"},
		Ignored:  "not-in-snapshot",
		Number:   2.5,
		Untagged: true,
		private:  "not-in-snapshot",
	}))
	if err != nil {
		t.Fatalf("could not serialize: %v", err)
	}

	want := `{"Untagged":true,"inherited":"from-embedded","nil_ptr":null,"number":2.5,"pointer":{"value":"nested"},"renamed":"renamed-value"}`
	if got != want {
		t.Fatalf("Normalize() = %s, want %s", got, want)
	}
}

func TestNormalizeBreaksCycles(t *testing.T) {
	type node struct {
		Name string
		Self *node
	}
	value := &node{Name: "root"}
	value.Self = value

	got, err := marshalValue(Normalize(value))
	if err != nil {
		t.Fatalf("could not serialize: %v", err)
	}
	want := `{"Name":"root","Self":"<recursion>"}`
	if got != want {
		t.Fatalf("Normalize() = %s, want %s", got, want)
	}
}

func TestNormalizeMapRejectsNonObject(t *testing.T) {
	if _, err := normalizeMap([]string{"a"}); err == nil {
		t.Fatal("expected a list to be rejected as snapshot state")
	}
}
