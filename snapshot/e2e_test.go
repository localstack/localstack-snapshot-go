package snapshot

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// queueResponse stands in for an API response with non-deterministic values.
type queueResponse struct {
	QueueURL  string    `json:"QueueUrl"`
	QueueArn  string    `json:"QueueArn"`
	CreatedAt time.Time `json:"CreatedAt"`
	Messages  []string  `json:"Messages"`
}

func response(id string, created time.Time) queueResponse {
	url := "http://localhost:4566/000000000000/queue-" + id
	return queueResponse{
		QueueURL:  url,
		QueueArn:  "arn:aws:sqs:us-east-1:000000000000:queue-" + id,
		CreatedAt: created,
		Messages:  []string{"hello from " + url},
	}
}

func transformers(snap *Session) {
	snap.AddTransformer(
		Transform.KeyValue("QueueUrl", "queue-url", true),
		Transform.KeyValue("QueueArn", "queue-arn", true),
		Transform.Timestamp(),
	)
}

func TestRecordThenVerify(t *testing.T) {
	base := filepath.Join(t.TempDir(), "sqs")

	// 1. record the snapshot
	recorder := &fakeT{name: "TestQueue"}
	snap := New(recorder, WithBaseFilePath(base), WithUpdate(true))
	transformers(snap)
	snap.Match("create-queue", response("aaa", time.Date(2024, 3, 1, 10, 0, 0, 0, time.UTC)))
	recorder.runCleanups()
	requireNoFailure(t, recorder)

	written, err := os.ReadFile(base + snapshotFileSuffix)
	if err != nil {
		t.Fatalf("expected the snapshot file to be written: %v", err)
	}
	if !strings.HasSuffix(string(written), "}\n") {
		t.Errorf("expected the snapshot file to end with a newline, got %q", string(written))
	}

	var recorded map[string]struct {
		RecordedDate    string         `json:"recorded-date"`
		RecordedContent map[string]any `json:"recorded-content"`
	}
	if err := json.Unmarshal(written, &recorded); err != nil {
		t.Fatalf("expected valid JSON in the snapshot file: %v", err)
	}
	scope, ok := recorded["TestQueue"]
	if !ok {
		t.Fatalf("expected the snapshot to be recorded under the test name, got %v", recorded)
	}
	if scope.RecordedDate == "" {
		t.Error("expected a recorded-date")
	}

	content, err := marshalValue(scope.RecordedContent["create-queue"])
	if err != nil {
		t.Fatalf("could not serialize the recorded content: %v", err)
	}
	want := `{"CreatedAt":"<timestamp:2022-07-13T13:48:01.000Z>","Messages":["hello from <queue-url:1>"],"QueueArn":"<queue-arn:1>","QueueUrl":"<queue-url:1>"}`
	if content != want {
		t.Fatalf("unexpected recorded content:\n%s\nwant:\n%s", content, want)
	}

	// 2. verify a run with different ids and timestamps against that recording
	verifier := &fakeT{name: "TestQueue"}
	snap = New(verifier, WithBaseFilePath(base), WithUpdate(false))
	transformers(snap)
	snap.Match("create-queue", response("aaa", time.Date(2025, 12, 24, 23, 59, 59, 0, time.UTC)))
	verifier.runCleanups()
	requireMatched(t, snap, verifier)

	// 3. a real difference fails
	failing := &fakeT{name: "TestQueue"}
	snap = New(failing, WithBaseFilePath(base), WithUpdate(false))
	transformers(snap)
	changed := response("aaa", time.Now())
	changed.Messages = []string{"different message"}
	snap.Match("create-queue", changed)
	failing.runCleanups()
	requireFailure(t, failing, "parity snapshot failed")
	requireFailure(t, failing, "$..Messages")
}

func TestRecordKeepsOtherScopes(t *testing.T) {
	base := filepath.Join(t.TempDir(), "sqs")

	for _, name := range []string{"TestOne", "TestTwo"} {
		recorder := &fakeT{name: name}
		snap := New(recorder, WithBaseFilePath(base), WithUpdate(true))
		snap.Match("key", map[string]any{"name": name})
		recorder.runCleanups()
		requireNoFailure(t, recorder)
	}

	data, err := os.ReadFile(base + snapshotFileSuffix)
	if err != nil {
		t.Fatalf("could not read the snapshot file: %v", err)
	}
	full, err := unmarshalMap(data)
	if err != nil {
		t.Fatalf("could not parse the snapshot file: %v", err)
	}
	if len(full) != 2 {
		t.Fatalf("expected both test scopes in the snapshot file, got %v", sortedKeys(full))
	}

	// re-recording one scope leaves the other one untouched
	recorder := &fakeT{name: "TestOne"}
	snap := New(recorder, WithBaseFilePath(base), WithUpdate(true))
	snap.Match("key", map[string]any{"name": "TestOne", "extra": true})
	recorder.runCleanups()

	data, _ = os.ReadFile(base + snapshotFileSuffix)
	full, _ = unmarshalMap(data)
	if len(full) != 2 {
		t.Fatalf("expected both test scopes to survive, got %v", sortedKeys(full))
	}
	if _, ok := full["TestTwo"]; !ok {
		t.Fatal("expected the other scope to be untouched")
	}
	_ = snap
}

func TestVerifyWithoutRecordingFails(t *testing.T) {
	base := filepath.Join(t.TempDir(), "missing")

	verifier := &fakeT{name: "TestMissing"}
	snap := New(verifier, WithBaseFilePath(base), WithUpdate(false))
	snap.Match("key", map[string]any{"a": 1})
	verifier.runCleanups()

	requireFailure(t, verifier, "Please (re-)generate the snapshot")
}

func TestRawSnapshotFile(t *testing.T) {
	base := filepath.Join(t.TempDir(), "raw")

	recorder := &fakeT{name: "TestRaw"}
	snap := New(recorder, WithBaseFilePath(base), WithUpdate(true), WithRaw(true))
	snap.AddTransformer(Transform.KeyValue("QueueUrl", "queue-url", true))
	snap.Match("create-queue", map[string]any{"QueueUrl": "http://localhost:4566/queue"})
	recorder.runCleanups()
	requireNoFailure(t, recorder)

	raw, err := os.ReadFile(base + rawFileSuffix)
	if err != nil {
		t.Fatalf("expected the raw snapshot file to be written: %v", err)
	}
	if !strings.Contains(string(raw), "http://localhost:4566/queue") {
		t.Fatalf("expected the raw snapshot to keep the untransformed value, got:\n%s", raw)
	}

	transformed, err := os.ReadFile(base + snapshotFileSuffix)
	if err != nil {
		t.Fatalf("could not read the snapshot file: %v", err)
	}
	if !strings.Contains(string(transformed), "<queue-url:1>") {
		t.Fatalf("expected the snapshot to be transformed, got:\n%s", transformed)
	}
}

func TestUpdateModeFromEnvironment(t *testing.T) {
	for value, want := range map[string]bool{"y": true, "Y": true, "": false, "1": false, "yes": false, "n": false} {
		t.Setenv(UpdateEnvVar, value)
		if got := UpdateMode(); got != want {
			t.Errorf("with %s=%q UpdateMode() = %v, want %v", UpdateEnvVar, value, got, want)
		}
	}

	t.Setenv(UpdateEnvVar, "y")
	ft := &fakeT{name: "TestEnv"}
	snap := New(ft, WithBaseFilePath(filepath.Join(t.TempDir(), "env")))
	if !snap.Updating() {
		t.Fatalf("expected the session to record when %s=y", UpdateEnvVar)
	}
}

func TestSkipAllFromEnvironment(t *testing.T) {
	t.Setenv(SkipAllEnvVar, "1")

	ft := &fakeT{name: "TestSkipAll"}
	snap := New(ft,
		WithBaseFilePath(filepath.Join(t.TempDir(), "skip-all")),
		WithScopeKey("TestSkipAll"),
		WithRecordedState(map[string]any{"key": map[string]any{"a": json.Number("1")}}),
	)
	snap.Match("key", map[string]any{"a": 2})
	ft.runCleanups()

	requireNoFailure(t, ft)
	if len(snap.Results()) != 0 {
		t.Fatalf("expected verification to be skipped, got %d results", len(snap.Results()))
	}
}

func TestDebugLogging(t *testing.T) {
	t.Setenv(DebugEnvVar, "1")

	ft := &fakeT{name: "TestDebug"}
	snap := New(ft,
		WithBaseFilePath(filepath.Join(t.TempDir(), "debug")),
		WithUpdate(true),
		WithTransformers(Transform.KeyValue("QueueUrl", "queue-url", true)),
	)
	snap.Match("key", map[string]any{"QueueUrl": "http://localhost:4566/queue"})
	ft.runCleanups()

	requireNoFailure(t, ft)
	if len(ft.logs) == 0 || !strings.Contains(strings.Join(ft.logs, "\n"), "queue-url:1") {
		t.Fatalf("expected the transformer to log its replacement, got %v", ft.logs)
	}
}
