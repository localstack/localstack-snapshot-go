package snapshot

import (
	"fmt"
	"strings"
	"testing"
)

// fakeT captures what a session reports, so that the library can test its own failure behaviour.
type fakeT struct {
	name     string
	errors   []string
	logs     []string
	cleanups []func()
}

func (f *fakeT) Helper() {}

func (f *fakeT) Name() string { return f.name }

func (f *fakeT) Cleanup(fn func()) { f.cleanups = append(f.cleanups, fn) }

func (f *fakeT) Errorf(format string, args ...any) {
	f.errors = append(f.errors, fmt.Sprintf(format, args...))
}

func (f *fakeT) Logf(format string, args ...any) {
	f.logs = append(f.logs, fmt.Sprintf(format, args...))
}

// runCleanups runs the registered cleanups in LIFO order, like testing.T does at the end of a test.
func (f *fakeT) runCleanups() {
	for i := len(f.cleanups) - 1; i >= 0; i-- {
		f.cleanups[i]()
	}
	f.cleanups = nil
}

func (f *fakeT) failed() bool { return len(f.errors) > 0 }

func (f *fakeT) errorText() string { return strings.Join(f.errors, "\n") }

// newSession creates a verifying session with a preset recorded state.
func newSession(t *testing.T, recorded map[string]any, opts ...Option) (*Session, *fakeT) {
	t.Helper()

	state, err := normalizeMap(recorded)
	if err != nil {
		t.Fatalf("could not normalize the recorded state: %v", err)
	}

	ft := &fakeT{name: "A"}
	options := append([]Option{
		WithScopeKey("A"),
		WithRecordedState(state),
		WithUpdate(false),
		WithVerify(true),
		WithBaseFilePath(""),
	}, opts...)
	return New(ft, options...), ft
}

func requireNoFailure(t *testing.T, ft *fakeT) {
	t.Helper()
	if ft.failed() {
		t.Fatalf("expected the snapshot to match, got:\n%s", ft.errorText())
	}
}

// requireMatched asserts that the snapshot matched and that verification actually ran, so that a
// silently skipped comparison cannot pass as a success.
func requireMatched(t *testing.T, snap *Session, ft *fakeT) {
	t.Helper()
	requireNoFailure(t, ft)
	if len(snap.Results()) == 0 {
		t.Fatalf("expected the snapshot to be verified, but no keys were compared")
	}
}

func requireFailure(t *testing.T, ft *fakeT, substring string) {
	t.Helper()
	if !ft.failed() {
		t.Fatalf("expected a snapshot failure containing %q, but the snapshot matched", substring)
	}
	if !strings.Contains(ft.errorText(), substring) {
		t.Fatalf("expected a snapshot failure containing %q, got:\n%s", substring, ft.errorText())
	}
}
