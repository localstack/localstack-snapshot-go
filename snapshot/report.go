package snapshot

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
)

var regularJSONPathChars = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

const (
	colorReset  = "\x1b[0m"
	colorRed    = "\x1b[31m"
	colorGreen  = "\x1b[32m"
	colorYellow = "\x1b[33m"
	colorCyan   = "\x1b[36m"
)

// formatJSONPath renders a change path as a json path that can be pasted into the skip list.
func formatJSONPath(path []any) string {
	jsonPath := "$.."
	for idx, elem := range path {
		if _, isIndex := elem.(int); !isIndex {
			part := fmt.Sprint(elem)
			// wrap parts with special characters in single quotes so they can be copy-pasted as is
			if !regularJSONPathChars.MatchString(part) {
				part = "'" + part + "'"
			}
			jsonPath += part
		}
		if idx < len(path)-1 && !strings.HasSuffix(jsonPath, "..") {
			jsonPath += "."
		}
	}
	if len(path) > 0 {
		if _, isIndex := path[len(path)-1].(int); isIndex {
			jsonPath = strings.TrimRight(jsonPath, ".")
		}
	}
	return `"` + jsonPath + `"`
}

// RenderReport renders a failed match result as a human readable diff, including the list of json
// paths that can be used to skip verification of the offending values.
func RenderReport(result MatchResult) string {
	return renderReport(result, colorEnabled())
}

func renderReport(result MatchResult, color bool) string {
	var builder strings.Builder
	fmt.Fprintf(&builder, ">> match key: %s\n", result.Key)

	for _, change := range result.Changes {
		for _, line := range renderChange(change, color) {
			fmt.Fprintf(&builder, "\t%s\n", line)
		}
	}

	paths := map[string]bool{}
	for _, change := range result.Changes {
		paths[formatJSONPath(change.Path)] = true
	}
	sorted := make([]string, 0, len(paths))
	for path := range paths {
		sorted = append(sorted, path)
	}
	sort.Strings(sorted)

	builder.WriteString("\n\tIgnore list (please keep in mind list indices might not work and should be replaced):\n\t")
	fmt.Fprintf(&builder, "[%s]", strings.Join(sorted, ", "))
	return builder.String()
}

func renderChange(change Change, color bool) []string {
	path := renderPath(change.Path)

	switch change.Type {
	case MapItemRemoved:
		return []string{fmt.Sprintf("%s %s ( %s )", colorize("(-)", colorRed, color), path, render(change.Expected))}
	case ListItemRemoved:
		return []string{fmt.Sprintf("%s %s ( %s )", colorize("(-)", colorRed, color), path, render(change.Expected))}
	case MapItemAdded, ListItemAdded:
		return []string{fmt.Sprintf("%s %s ( %s )", colorize("(+)", colorGreen, color), path, render(change.Actual))}
	case ValuesChanged:
		return []string{fmt.Sprintf(
			"%s %s %s → %s ... (expected → actual)",
			colorize("(~)", colorYellow, color), path, render(change.Expected), render(change.Actual),
		)}
	case TypeChanged:
		return []string{fmt.Sprintf(
			"%s %s %s (type: %s) → %s (type: %s)... (expected → actual)",
			colorize("(~)", colorYellow, color), path,
			render(change.Expected), jsonTypeName(change.Expected),
			render(change.Actual), jsonTypeName(change.Actual),
		)}
	}

	return []string{fmt.Sprintf(
		"%s %s Unsupported diff mismatch for %s vs %s",
		colorize("?", colorCyan, color), path, render(change.Expected), render(change.Actual),
	)}
}

// render formats a value for the report: strings are quoted, everything else is shown as JSON.
func render(value any) string {
	if str, ok := value.(string); ok {
		return fmt.Sprintf("%q", str)
	}
	if value == nil {
		return "null"
	}
	out, err := marshalValue(value)
	if err != nil {
		return fmt.Sprint(value)
	}
	return out
}

func colorize(text, color string, enabled bool) string {
	if !enabled {
		return text
	}
	return color + text + colorReset
}

// colorEnabled reports whether ANSI colors should be used: only on a terminal, and never when
// NO_COLOR is set (https://no-color.org).
func colorEnabled() bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	info, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}
