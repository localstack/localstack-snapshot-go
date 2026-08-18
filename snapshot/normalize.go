package snapshot

import (
	"encoding"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"reflect"
	"strconv"
	"strings"
	"time"
)

// maxNormalizeDepth guards against pathologically deep (or self-referential) structures.
const maxNormalizeDepth = 96

// timestampLayout is the canonical layout used for time.Time values. It matches the format
// produced by the Python library (`%Y-%m-%dT%H:%M:%S.%fZ`, truncated to milliseconds) and is
// recognized by the TimestampTransformer.
const timestampLayout = "2006-01-02T15:04:05.000Z"

// Normalize converts an arbitrary Go value into the JSON-like value space that snapshots are
// recorded in: nil, bool, string, json.Number, map[string]any and []any.
//
// The conversion mirrors what a JSON API would emit, with a few snapshot specific conveniences:
//
//   - time.Time becomes a canonical millisecond timestamp string
//   - io.Reader (e.g. an S3 object body) is drained and becomes a string
//   - []byte becomes a string
//   - error becomes its Error() message
//   - json.Marshaler / encoding.TextMarshaler are honoured
//   - struct fields respect `json` tags (including omitempty, "-" and embedded inlining)
//   - all numbers become json.Number so that ints and floats survive a snapshot round-trip
func Normalize(v any) any {
	return normalizeValue(reflect.ValueOf(v), 0, nil)
}

func normalizeValue(v reflect.Value, depth int, seen []uintptr) any {
	if depth > maxNormalizeDepth {
		return "<max-depth-exceeded>"
	}
	if !v.IsValid() {
		return nil
	}

	switch v.Kind() {
	case reflect.Ptr, reflect.Interface:
		if v.IsNil() {
			return nil
		}
	}

	// Handle values that carry their own representation before falling back to structural walking.
	if v.CanInterface() {
		switch iface := v.Interface().(type) {
		case json.Number:
			return iface
		case time.Time:
			return iface.UTC().Format(timestampLayout)
		case json.RawMessage:
			if parsed, err := unmarshalValue(string(iface)); err == nil {
				return parsed
			}
			return string(iface)
		case io.Reader:
			data, err := io.ReadAll(iface)
			if err != nil {
				return fmt.Sprintf("<unreadable-stream: %s>", err)
			}
			return string(data)
		case json.Marshaler:
			data, err := iface.MarshalJSON()
			if err != nil {
				return fmt.Sprintf("<unmarshalable: %s>", err)
			}
			if parsed, err := unmarshalValue(string(data)); err == nil {
				return parsed
			}
			return string(data)
		case encoding.TextMarshaler:
			data, err := iface.MarshalText()
			if err != nil {
				return fmt.Sprintf("<unmarshalable: %s>", err)
			}
			return string(data)
		case error:
			return iface.Error()
		}
	}

	switch v.Kind() {
	case reflect.Ptr, reflect.Interface:
		if v.Kind() == reflect.Ptr {
			ptr := v.Pointer()
			if containsPtr(seen, ptr) {
				return "<recursion>"
			}
			seen = append(seen, ptr)
		}
		return normalizeValue(v.Elem(), depth, seen)

	case reflect.Bool:
		return v.Bool()

	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return json.Number(strconv.FormatInt(v.Int(), 10))

	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return json.Number(strconv.FormatUint(v.Uint(), 10))

	case reflect.Float32, reflect.Float64:
		return normalizeFloat(v.Float())

	case reflect.Complex64, reflect.Complex128:
		return fmt.Sprint(v.Complex())

	case reflect.String:
		return v.String()

	case reflect.Slice:
		if v.IsNil() {
			return nil
		}
		if v.Type().Elem().Kind() == reflect.Uint8 {
			return string(v.Bytes())
		}
		ptr := v.Pointer()
		if containsPtr(seen, ptr) {
			return "<recursion>"
		}
		seen = append(seen, ptr)
		return normalizeList(v, depth, seen)

	case reflect.Array:
		return normalizeList(v, depth, seen)

	case reflect.Map:
		if v.IsNil() {
			return nil
		}
		ptr := v.Pointer()
		if containsPtr(seen, ptr) {
			return "<recursion>"
		}
		seen = append(seen, ptr)
		out := make(map[string]any, v.Len())
		iter := v.MapRange()
		for iter.Next() {
			out[mapKeyString(iter.Key())] = normalizeValue(iter.Value(), depth+1, seen)
		}
		return out

	case reflect.Struct:
		out := make(map[string]any)
		addStructFields(v, out, depth, seen)
		return out

	case reflect.Chan, reflect.Func, reflect.UnsafePointer:
		return nil
	}

	return fmt.Sprint(v.Interface())
}

func normalizeList(v reflect.Value, depth int, seen []uintptr) []any {
	out := make([]any, v.Len())
	for i := 0; i < v.Len(); i++ {
		out[i] = normalizeValue(v.Index(i), depth+1, seen)
	}
	return out
}

// normalizeFloat renders a float the way encoding/json would, as a json.Number.
func normalizeFloat(f float64) any {
	if math.IsNaN(f) {
		return "NaN"
	}
	if math.IsInf(f, 1) {
		return "+Inf"
	}
	if math.IsInf(f, -1) {
		return "-Inf"
	}
	abs := math.Abs(f)
	format := byte('f')
	if abs != 0 && (abs < 1e-6 || abs >= 1e21) {
		format = 'e'
	}
	return json.Number(strconv.FormatFloat(f, format, -1, 64))
}

func addStructFields(v reflect.Value, out map[string]any, depth int, seen []uintptr) {
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		name, opts := parseJSONTag(field.Tag.Get("json"))

		if field.Anonymous && name == "" {
			embedded := v.Field(i)
			for embedded.Kind() == reflect.Ptr {
				if embedded.IsNil() {
					embedded = reflect.Value{}
					break
				}
				embedded = embedded.Elem()
			}
			if embedded.IsValid() && embedded.Kind() == reflect.Struct {
				// Embedded structs are inlined, like encoding/json does.
				addStructFields(embedded, out, depth, seen)
				continue
			}
		}

		if field.PkgPath != "" {
			continue // unexported
		}
		if name == "-" {
			continue
		}
		value := v.Field(i)
		if opts.contains("omitempty") && isEmptyValue(value) {
			continue
		}
		if name == "" {
			name = field.Name
		}
		out[name] = normalizeValue(value, depth+1, seen)
	}
}

type tagOptions string

func (o tagOptions) contains(option string) bool {
	for _, candidate := range strings.Split(string(o), ",") {
		if candidate == option {
			return true
		}
	}
	return false
}

func parseJSONTag(tag string) (string, tagOptions) {
	name, opts, _ := strings.Cut(tag, ",")
	return name, tagOptions(opts)
}

func isEmptyValue(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.Array, reflect.Map, reflect.Slice, reflect.String:
		return v.Len() == 0
	case reflect.Bool:
		return !v.Bool()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return v.Int() == 0
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return v.Uint() == 0
	case reflect.Float32, reflect.Float64:
		return v.Float() == 0
	case reflect.Interface, reflect.Ptr:
		return v.IsNil()
	}
	return false
}

func mapKeyString(k reflect.Value) string {
	if k.Kind() == reflect.String {
		return k.String()
	}
	if k.CanInterface() {
		if tm, ok := k.Interface().(encoding.TextMarshaler); ok {
			if data, err := tm.MarshalText(); err == nil {
				return string(data)
			}
		}
		return fmt.Sprint(k.Interface())
	}
	return fmt.Sprint(k)
}

func containsPtr(seen []uintptr, ptr uintptr) bool {
	for _, s := range seen {
		if s == ptr {
			return true
		}
	}
	return false
}

// normalizeMap normalizes a value and asserts that the result is a JSON object. Snapshot state is
// always keyed by strings, so the top level of the state is guaranteed to be a map.
func normalizeMap(v any) (map[string]any, error) {
	normalized := Normalize(v)
	if normalized == nil {
		return map[string]any{}, nil
	}
	asMap, ok := normalized.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("expected a JSON object, got %T", normalized)
	}
	return asMap, nil
}
