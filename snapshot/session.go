// Package snapshot provides snapshot testing for Go, modelled after localstack's python snapshot
// library.
//
// A snapshot test records the state it observes into a `<package>/<file>_test.snapshot.json` file
// next to the test, and compares against that recording on subsequent runs. The mode is selected by
// the UPDATE_SNAPSHOT environment variable:
//
//	UPDATE_SNAPSHOT=y go test ./...   # records snapshots
//	go test ./...                     # verifies against the recorded snapshots
//
// Usage:
//
//	func TestCreateQueue(t *testing.T) {
//		snap := snapshot.New(t)
//		snap.AddTransformer(snapshot.Transform.KeyValue("QueueUrl", "", true))
//
//		out, err := client.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: &name})
//		require.NoError(t, err)
//
//		snap.Match("create-queue", out)
//	}
//
// The session verifies (or records) itself when the test ends, via t.Cleanup.
package snapshot

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	// UpdateEnvVar toggles snapshot recording. Set it to "y" to record, anything else verifies.
	UpdateEnvVar = "UPDATE_SNAPSHOT"
	// RawEnvVar set to "1" additionally writes an untransformed snapshot file, useful for debugging
	// transformers.
	RawEnvVar = "SNAPSHOT_RAW"
	// SkipAllEnvVar set to "1" disables snapshot verification entirely.
	SkipAllEnvVar = "SNAPSHOT_SKIP_ALL"
	// DebugEnvVar set to any value logs what the transformers are doing.
	DebugEnvVar = "DEBUG_SNAPSHOT"

	snapshotFileSuffix = ".snapshot.json"
	rawFileSuffix      = ".raw.snapshot.json"

	recordedContentKey = "recorded-content"
	recordedDateKey    = "recorded-date"

	// skipPlaceholder marks list items that are removed because their path is skipped. Removal has to
	// happen in a second pass, otherwise the indices of the remaining skip paths would shift.
	skipPlaceholder = "$__to_be_skipped__$"
)

// TestingT is the part of *testing.T that a session uses.
type TestingT interface {
	Helper()
	Name() string
	Cleanup(func())
	Errorf(format string, args ...any)
	Logf(format string, args ...any)
}

// MatchResult is the comparison of one matched key against its recording.
type MatchResult struct {
	Key      string
	Expected any
	Actual   any
	Changes  []Change
}

// Failed reports whether the observed state differs from the recording.
func (r MatchResult) Failed() bool {
	return len(r.Changes) > 0
}

// String renders the result as a diff report.
func (r MatchResult) String() string {
	return RenderReport(r)
}

type registeredTransformer struct {
	transformer Transformer
	priority    int
	order       int
}

// Session records and verifies the snapshots of a single test. It is created with New and finishes
// itself when the test ends.
type Session struct {
	t        TestingT
	scopeKey string

	filePath    string
	rawFilePath string

	update bool
	verify bool
	raw    bool

	mu                    sync.Mutex
	calledKeys            []string
	observed              map[string]any
	recorded              map[string]any
	stateLoaded           bool
	transformers          []registeredTransformer
	skipVerificationPaths []string
	verifyDisabled        bool
	results               []MatchResult
	done                  bool
}

// Option configures a Session.
type Option func(*Session)

// WithBaseFilePath overrides where the snapshot file lives. The suffix ".snapshot.json" is appended,
// so a base path of "testdata/sqs" results in "testdata/sqs.snapshot.json". By default the snapshot
// file sits next to the test file that created the session.
func WithBaseFilePath(basePath string) Option {
	return func(s *Session) { s.setBaseFilePath(basePath) }
}

// WithScopeKey overrides the key the snapshot is recorded under. Defaults to the test name.
func WithScopeKey(scopeKey string) Option {
	return func(s *Session) { s.scopeKey = scopeKey }
}

// WithUpdate forces recording (true) or verifying (false), ignoring UPDATE_SNAPSHOT.
func WithUpdate(update bool) Option {
	return func(s *Session) { s.update = update }
}

// WithVerify enables or disables verification, ignoring SNAPSHOT_SKIP_ALL.
func WithVerify(verify bool) Option {
	return func(s *Session) { s.verify = verify }
}

// WithRaw additionally writes an untransformed snapshot file, ignoring SNAPSHOT_RAW.
func WithRaw(raw bool) Option {
	return func(s *Session) { s.raw = raw }
}

// WithTransformers adds transformers up front.
func WithTransformers(transformers ...Transformer) Option {
	return func(s *Session) {
		for _, transformer := range transformers {
			s.addTransformer(transformer, 0)
		}
	}
}

// WithRecordedState presets the recorded state instead of loading it from the snapshot file. Mostly
// useful for testing the snapshot library itself.
func WithRecordedState(state map[string]any) Option {
	return func(s *Session) {
		s.recorded = state
		s.stateLoaded = true
	}
}

// New creates a snapshot session for a test. The snapshot file defaults to the test file's name with
// a ".snapshot.json" suffix, and the snapshot is recorded under the test name.
//
// The returned session verifies itself (or writes the recording) when the test ends.
func New(t TestingT, opts ...Option) *Session {
	t.Helper()

	session := &Session{
		t:        t,
		scopeKey: t.Name(),
		update:   UpdateMode(),
		verify:   os.Getenv(SkipAllEnvVar) != "1",
		raw:      os.Getenv(RawEnvVar) == "1",
		observed: map[string]any{},
		recorded: map[string]any{},
	}
	session.setBaseFilePath(callerBaseFilePath())

	for _, opt := range opts {
		opt(session)
	}

	if !session.stateLoaded {
		session.loadState()
	}

	t.Cleanup(session.finish)
	return session
}

// UpdateMode reports whether snapshots are being recorded, i.e. whether UPDATE_SNAPSHOT is set to
// "y". Tests can use it to skip assertions that only make sense while verifying.
func UpdateMode() bool {
	return strings.EqualFold(os.Getenv(UpdateEnvVar), "y")
}

func (s *Session) setBaseFilePath(basePath string) {
	if basePath == "" {
		s.filePath = ""
		s.rawFilePath = ""
		return
	}
	s.filePath = basePath + snapshotFileSuffix
	s.rawFilePath = basePath + rawFileSuffix
}

// FilePath returns the snapshot file this session reads from and writes to.
func (s *Session) FilePath() string {
	return s.filePath
}

// ScopeKey returns the key the snapshot is recorded under.
func (s *Session) ScopeKey() string {
	return s.scopeKey
}

// Updating reports whether this session records instead of verifies.
func (s *Session) Updating() bool {
	return s.update
}

// AddTransformer registers transformers with the default priority (0). Transformers are applied in
// order of ascending priority, and in registration order within the same priority.
func (s *Session) AddTransformer(transformers ...Transformer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, transformer := range transformers {
		s.addTransformer(transformer, 0)
	}
}

// AddTransformerWithPriority registers a transformer that runs before (lower priority) or after
// (higher priority) the transformers registered with the default priority.
func (s *Session) AddTransformerWithPriority(transformer Transformer, priority int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.addTransformer(transformer, priority)
}

func (s *Session) addTransformer(transformer Transformer, priority int) {
	if transformer == nil {
		return
	}
	s.transformers = append(s.transformers, registeredTransformer{
		transformer: transformer,
		priority:    priority,
		order:       len(s.transformers),
	})
}

// Match records the given value under key, or compares it against the recording. Anything that can
// be marshalled to JSON works, including structs and AWS SDK output types; see Normalize.
//
// A key may only be used once per test. Comparison happens when the test ends.
func (s *Session) Match(key string, value any) {
	s.t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, called := range s.calledKeys {
		if called == key {
			s.t.Errorf("snapshot: key %q used multiple times in the same test scope", key)
			return
		}
	}

	normalized := Normalize(value)
	s.calledKeys = append(s.calledKeys, key)
	s.observed[key] = normalized

	if _, recorded := s.recorded[key]; !s.update && !recorded {
		s.t.Errorf(
			"snapshot: no state for %q recorded in %s. Please (re-)generate the snapshot for this test with %s=y",
			key, s.snapshotLocation(), UpdateEnvVar,
		)
	}
}

// SkipVerify disables verification for this test, while still recording in update mode. The
// equivalent of the python library's `skip_snapshot_verify` marker without paths.
func (s *Session) SkipVerify() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.verifyDisabled = true
}

// SkipVerifyPaths excludes the given json paths from verification, e.g. "$..CreateDate" or
// "$..Records[0].Body". The equivalent of the python library's `skip_snapshot_verify(paths=[...])`.
func (s *Session) SkipVerifyPaths(paths ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.skipVerificationPaths = append(s.skipVerificationPaths, paths...)
}

// Results returns the comparison results, available after the session finished.
func (s *Session) Results() []MatchResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]MatchResult(nil), s.results...)
}

// AssertAll verifies (or records) the snapshot immediately instead of waiting for the test to end.
// Calling it more than once, or after the test ended, is a no-op.
func (s *Session) AssertAll() {
	s.t.Helper()
	s.finish()
}

func (s *Session) finish() {
	s.t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.done {
		return
	}
	s.done = true

	if len(s.observed) == 0 {
		// Match was never called, so this is not a "real" snapshot test: a session might be created by
		// a shared helper that only some tests actually use.
		return
	}

	if !s.update {
		if err := s.removeSkipVerificationPaths(s.recorded); err != nil {
			s.t.Errorf("snapshot: %v", err)
			return
		}
	}

	transformed, err := s.transform(s.observed)
	if err != nil {
		s.t.Errorf("snapshot: %v", err)
		return
	}
	s.observed = transformed

	if s.update {
		if err := s.persist(); err != nil {
			s.t.Errorf("snapshot: could not write %s: %v", s.filePath, err)
		}
		return
	}

	if !s.verify {
		s.debugf("snapshot verification disabled")
		return
	}
	if s.verifyDisabled {
		s.debugf("snapshot verification disabled for this test case")
		return
	}
	if len(s.skipVerificationPaths) > 0 {
		s.debugf("snapshot verification disabled for paths: %s", strings.Join(s.skipVerificationPaths, ", "))
	}

	if len(s.recorded) == 0 {
		s.t.Errorf(
			"snapshot: no state for %q recorded. Please (re-)generate the snapshot for this test with %s=y",
			s.scopeKey, UpdateEnvVar,
		)
		return
	}

	var reports []string
	for _, key := range s.calledKeys {
		expected, ok := s.recorded[key]
		if !ok {
			// a new key was added since the snapshot was last recorded
			s.t.Errorf(
				"snapshot: state for key %q missing in %q. Please (re-)generate the snapshot for this test with %s=y",
				key, s.scopeKey, UpdateEnvVar,
			)
			continue
		}

		result := MatchResult{Key: key, Expected: expected, Actual: transformed[key]}
		result.Changes = Diff(result.Expected, result.Actual)
		s.results = append(s.results, result)
		if result.Failed() {
			reports = append(reports, RenderReport(result))
		}
	}

	if len(reports) > 0 {
		s.t.Errorf("snapshot: parity snapshot failed\n%s", strings.Join(reports, "\n"))
	}
}

// transform builds the persistable state definition that can later be compared against.
func (s *Session) transform(state map[string]any) (map[string]any, error) {
	parseEmbeddedJSON(state)

	if s.raw {
		if err := s.persistRaw(state); err != nil {
			return nil, fmt.Errorf("could not write %s: %w", s.rawFilePath, err)
		}
	}

	ctx := NewTransformContext()
	ctx.logf = s.debugf

	for _, registered := range s.sortedTransformers() {
		transformed := registered.transformer.Transform(state, ctx)
		if transformed != nil {
			state = transformed
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	if !s.update {
		if err := s.removeSkipVerificationPaths(state); err != nil {
			return nil, err
		}
	}

	// Apply the registered replacements on the serialized state, but per key, so that replacements
	// never affect the snapshot keys themselves.
	result := make(map[string]any, len(state))
	for key, value := range state {
		serialized, err := marshalValue(value)
		if err != nil {
			return nil, fmt.Errorf("could not serialize snapshot value for key %q: %w", key, err)
		}
		for _, replace := range ctx.SerializedReplacements() {
			serialized = replace(serialized)
		}
		parsed, err := unmarshalValue(serialized)
		if err != nil {
			return nil, fmt.Errorf("could not decode the transformed snapshot value for key %q: %w", key, err)
		}
		result[key] = parsed
	}

	return result, nil
}

func (s *Session) sortedTransformers() []registeredTransformer {
	sorted := append([]registeredTransformer(nil), s.transformers...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].priority != sorted[j].priority {
			return sorted[i].priority < sorted[j].priority
		}
		return sorted[i].order < sorted[j].order
	})
	return sorted
}

// parseEmbeddedJSON walks the state and parses string values that contain a JSON object, so that
// transformers can be applied to their contents. Note that this only covers objects; use
// Transform.JSONString for lists and nested JSON strings.
func parseEmbeddedJSON(value any) any {
	switch container := value.(type) {
	case map[string]any:
		for key, nested := range container {
			container[key] = parseEmbeddedJSON(nested)
		}
		return container
	case []any:
		for i, item := range container {
			container[i] = parseEmbeddedJSON(item)
		}
		return container
	case string:
		if !strings.HasPrefix(container, "{") {
			return container
		}
		parsed, err := unmarshalValue(container)
		if err != nil {
			return container // parsing errors can be ignored
		}
		return parsed
	default:
		return value
	}
}

// removeSkipVerificationPaths removes all values matching the skipped json paths from the state.
func (s *Session) removeSkipVerificationPaths(state map[string]any) error {
	if len(s.skipVerificationPaths) == 0 || len(state) == 0 {
		return nil
	}

	var errs []error
	hasPlaceholder := false
	for _, rawPath := range s.skipVerificationPaths {
		path, err := parseJSONPath(rawPath)
		if err != nil {
			errs = append(errs, fmt.Errorf("invalid skip verification path %q: %w", rawPath, err))
			continue
		}
		for _, match := range path.find(state) {
			switch parent := match.parent.(type) {
			case map[string]any:
				delete(parent, match.key)
			case []any:
				parent[match.index] = skipPlaceholder
				hasPlaceholder = true
			default:
				s.debugf("snapshot skip path %q was not applied as it was invalid for that snapshot", rawPath)
			}
		}
	}

	if hasPlaceholder {
		removeSkipPlaceholders(state)
	}
	return errors.Join(errs...)
}

func removeSkipPlaceholders(value any) any {
	switch container := value.(type) {
	case map[string]any:
		for key, nested := range container {
			container[key] = removeSkipPlaceholders(nested)
		}
		return container
	case []any:
		filtered := make([]any, 0, len(container))
		for _, item := range container {
			if str, ok := item.(string); ok && str == skipPlaceholder {
				continue
			}
			filtered = append(filtered, removeSkipPlaceholders(item))
		}
		return filtered
	default:
		return value
	}
}

func (s *Session) persist() error {
	return s.writeSnapshotFile(s.filePath, s.observed)
}

func (s *Session) persistRaw(state map[string]any) error {
	return s.writeSnapshotFile(s.rawFilePath, state)
}

// fileLock serializes the read-modify-write cycles on snapshot files, which parallel tests of the
// same package would otherwise race on.
var fileLock sync.Mutex

func (s *Session) writeSnapshotFile(path string, content map[string]any) error {
	if path == "" {
		return errors.New("no snapshot file path; pass snapshot.WithBaseFilePath to set one")
	}

	fileLock.Lock()
	defer fileLock.Unlock()

	existing, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	full, err := unmarshalMap(existing)
	if err != nil {
		return fmt.Errorf("existing snapshot file is not valid JSON: %w", err)
	}

	full[s.scopeKey] = map[string]any{
		recordedDateKey:    time.Now().UTC().Format("02-01-2006, 15:04:05"),
		recordedContentKey: content,
	}

	data, err := marshalIndented(full)
	if err != nil {
		return err
	}
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return os.WriteFile(path, data, 0o644)
}

func (s *Session) loadState() {
	s.stateLoaded = true
	s.recorded = map[string]any{}
	if s.filePath == "" {
		return
	}

	data, err := os.ReadFile(s.filePath)
	if err != nil {
		if !os.IsNotExist(err) {
			s.t.Errorf("snapshot: could not read %s: %v", s.filePath, err)
		}
		return
	}

	full, err := unmarshalMap(data)
	if err != nil {
		s.t.Errorf("snapshot: could not parse %s: %v", s.filePath, err)
		return
	}

	scope, ok := asMap(full[s.scopeKey])
	if !ok {
		return
	}
	if content, ok := asMap(scope[recordedContentKey]); ok {
		s.recorded = content
	}
}

func (s *Session) snapshotLocation() string {
	if s.filePath == "" {
		return s.scopeKey
	}
	return fmt.Sprintf("%s (%s)", s.filePath, s.scopeKey)
}

func (s *Session) debugf(format string, args ...any) {
	if os.Getenv(DebugEnvVar) == "" {
		return
	}
	s.t.Logf("snapshot: "+format, args...)
}

// packageDir is the directory of this package's sources, used to find the calling test file.
var packageDir = func() string {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return ""
	}
	return filepath.Dir(file)
}()

// callerBaseFilePath returns the path of the test file that created the session, without the ".go"
// suffix. Returns an empty string if it cannot be determined, in which case WithBaseFilePath is
// required.
func callerBaseFilePath() string {
	pc := make([]uintptr, 32)
	n := runtime.Callers(2, pc)
	frames := runtime.CallersFrames(pc[:n])
	for {
		frame, more := frames.Next()
		if frame.File != "" && filepath.Dir(frame.File) != packageDir && !isRuntimeFile(frame.File) {
			return strings.TrimSuffix(frame.File, ".go")
		}
		if !more {
			return ""
		}
	}
}

func isRuntimeFile(file string) bool {
	dir := filepath.Base(filepath.Dir(file))
	return dir == "testing" || dir == "runtime"
}
