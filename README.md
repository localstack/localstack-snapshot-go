# localstack-snapshot-go

Snapshot testing for Go, modelled after LocalStack's Python
[localstack-snapshot](https://github.com/localstack/localstack-snapshot) library.

A snapshot test records the state it observes into a JSON file next to the test, and compares against
that recording on every following run. Values that change between runs (ids, arns, urls, timestamps)
are normalized by *transformers* before they are recorded or compared.

The library has no dependencies outside the standard library.

## Install

```shell
go get github.com/localstack/localstack-snapshot-go
```

## Two modes, one environment variable

| Command                              | Mode                                                |
|--------------------------------------|-----------------------------------------------------|
| `UPDATE_SNAPSHOT=y go test ./...`    | **record**: (re-)writes the `.snapshot.json` files  |
| `go test ./...`                      | **verify**: compares against the recorded snapshots |

Anything other than `y` (case-insensitive) verifies.

## Quickstart

```go
func TestCreateQueue(t *testing.T) {
	snap := snapshot.New(t)
	snap.AddTransformer(
		snapshot.Transform.KeyValue("QueueUrl", "queue-url", true),
		snapshot.Transform.Timestamp(),
	)

	out, err := client.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: &name})
	require.NoError(t, err)

	snap.Match("create-queue", out)
}
```

Running this with `UPDATE_SNAPSHOT=y` writes `<your_test_file>_test.snapshot.json` next to the test:

```json
{
  "TestCreateQueue": {
    "recorded-content": {
      "create-queue": {
        "Attributes": {
          "CreatedTimestamp": "<timestamp:2022-07-13T13:48:01.000Z>",
          "QueueArn": "arn:aws:sqs:us-east-1:000000000000:my-queue"
        },
        "QueueUrl": "<queue-url:1>"
      }
    },
    "recorded-date": "18-08-2026, 10:23:15"
  }
}
```

The session verifies (or records) itself when the test ends, through `t.Cleanup`. Failures are
reported as a diff:

```
--- FAIL: TestCreateQueue (0.00s)
    queue_test.go:40: snapshot: parity snapshot failed
        >> match key: create-queue
        	(~) /Attributes/ApproximateNumberOfMessages 0 → 3 ... (expected → actual)
        	(+) /Attributes/NewField ( true )
        	(~) /Tags/[1]/Value "team-b" → "team-c" ... (expected → actual)

        	Ignore list (please keep in mind list indices might not work and should be replaced):
        	["$..Attributes.ApproximateNumberOfMessages", "$..Attributes.NewField", "$..Tags..Value"]
```

See [`example/queue_test.go`](example/queue_test.go) for a runnable example, including subtests and
skipped paths.

## Session API

| Method                                       | Description                                                                                     |
|----------------------------------------------|-------------------------------------------------------------------------------------------------|
| `snapshot.New(t, opts...)`                    | creates a session for a test; records or verifies when the test ends                            |
| `snap.Match(key, value)`                      | records/compares a value under `key`; each key may only be used once per test                   |
| `snap.AddTransformer(transformers...)`        | registers transformers                                                                          |
| `snap.AddTransformerWithPriority(tr, prio)`   | transformers run in ascending priority order (default `0`), registration order breaks ties      |
| `snap.SkipVerifyPaths(paths...)`              | excludes json paths from verification (they are still recorded)                                 |
| `snap.SkipVerify()`                           | disables verification for this test, while still recording in update mode                       |
| `snap.AssertAll()`                            | verifies immediately instead of waiting for the end of the test                                 |
| `snap.Results()`                              | the per-key comparison results, available after the session finished                            |
| `snapshot.UpdateMode()`                       | whether snapshots are being recorded                                                            |

`Match` accepts anything: maps, slices, structs (including AWS SDK output types), pointers, and
errors. Values are normalized to the JSON value space first (see `snapshot.Normalize`):

* `time.Time` becomes a canonical millisecond timestamp string, matched by the timestamp transformer
* `io.Reader` (an S3 body, for instance) is drained into a string
* `[]byte` becomes a string, `error` becomes its message
* `json.Marshaler` / `encoding.TextMarshaler` are honoured, struct fields respect their `json` tags
* all numbers become `json.Number`, so ints and floats survive the round-trip through the file
* self-referencing values are cut off with `<recursion>` instead of hanging

Options: `WithBaseFilePath`, `WithScopeKey`, `WithUpdate`, `WithVerify`, `WithRaw`,
`WithTransformers`, `WithRecordedState`.

## Transformers

Created through `snapshot.Transform` (the equivalent of the Python `TransformerUtility`). Go has no
keyword arguments, so the optional Python parameters are explicit:

```go
// replace the value of a key; "" derives the replacement from the key name ("FunctionName" ->
// "function-name"). The last argument is reference replacement: when true, every occurrence of the
// matched value in the whole snapshot becomes a numbered placeholder like <queue-url:1>.
snapshot.Transform.KeyValue("QueueUrl", "queue-url", true)
snapshot.Transform.KeyValueFunc("Body", func(key string, value any) string { ... }, false)
snapshot.Transform.KeyValueMatch(matchFn, "replacement", false)

snapshot.Transform.JSONPath("$..Attributes.QueueArn", "queue-arn", true)
snapshot.Transform.Regex(`i-[0-9a-f]{17}`, "<instance-id>")   // RE2 syntax, $1 for capture groups
snapshot.Transform.RegexCompiled(pattern, "<instance-id>")
snapshot.Transform.Text("amount: $4.00", "<amount>")          // literal, no escaping needed
snapshot.Transform.Timestamp()                                // <timestamp:2022-07-13T13:48:01Z>
snapshot.Transform.JSONString("Policy")                       // parse a JSON string into an object
snapshot.Transform.Sorting("Tags", func(a, b any) bool { ... })
snapshot.Transform.SortingByKey("Tags", "Key")
snapshot.Transform.ResponseMetadata()
snapshot.Transform.Generic(func(state map[string]any, ctx *snapshot.TransformContext) map[string]any { ... })
```

The transformer types (`KeyValueBasedTransformer`, `RegexTransformer`, ...) are exported as well, so
you can build them with struct literals, and implement your own:

```go
type Transformer interface {
	Transform(input map[string]any, ctx *TransformContext) map[string]any
}
```

A transformer either rewrites the state in place, or registers a replacement on the context
(`ctx.RegisterSerializedReplacement`) that is applied to the serialized snapshot afterwards. Numbered
placeholders come from `ctx.NewScope`. Errors are reported with `ctx.Errorf` and fail the test.

The legacy Python API is available as well: `snap.RegisterReplacement(re, value)`,
`snap.SkipKey(re, value)` and `snap.ReplaceValue(re, value)`.

## Skipping paths

`SkipVerifyPaths` takes json paths and is the equivalent of the Python `skip_snapshot_verify` marker.
The matched values are dropped from both sides before comparing, and they are still recorded, so you
can see them in the snapshot file.

```go
snap.SkipVerifyPaths(
	"$..RequestId",           // recursive descent
	"$..Records[0].Body",     // list index
	"$..Records.0.Body",      // same thing
	"$..Attributes.'a.b'",    // keys containing dots have to be quoted
	"$..Records[*].MD5",      // wildcard
)
```

Supported syntax: `$`, `.key`, `..key` (recursive descent), `[n]` / `.n` (list index, negative counts
from the end), `[*]` / `.*` (wildcard) and quoted keys (`.'a.b'`, `['a.b']`, `["a.b"]`). The ignore
list printed in a failure report is directly copy-pasteable.

## Environment variables

| Variable            | Effect                                                                     |
|---------------------|----------------------------------------------------------------------------|
| `UPDATE_SNAPSHOT=y` | record snapshots instead of verifying them                                 |
| `SNAPSHOT_RAW=1`    | also write a `.raw.snapshot.json` with the untransformed state (debugging)  |
| `SNAPSHOT_SKIP_ALL=1` | disable snapshot verification entirely                                    |
| `DEBUG_SNAPSHOT=1`  | log what the transformers are doing (via `t.Logf`)                         |
| `NO_COLOR=1`        | disable the ANSI colors in failure reports                                 |

## Differences from the Python library

The API surface follows the Python library, with these deliberate deviations:

* **No fixtures/plugins.** `snapshot.New(t)` replaces the pytest `snapshot` fixture, and the snapshot
  is scoped by `t.Name()` instead of the pytest node id. Subtests get their own scope
  (`TestFoo/subtest`).
* **`SkipVerify`/`SkipVerifyPaths` instead of markers.** Go has no test markers, so skipping is a
  method call on the session.
* **`Match` covers `match_object`.** Structs are normalized like an API would serialize them, so there
  is no separate object variant.
* **Sorted traversal.** Go maps are unordered, so the state is always traversed in sorted key order.
  Numbered reference placeholders are therefore stable, and snapshot files are written with sorted
  keys (the Python library keeps insertion order and moves `ResponseMetadata` last).
* **RE2 regexes.** No backreferences or lookaheads, and capture groups are `$1` rather than `\1`.
* **Failures are reported, not raised.** A mismatch calls `t.Errorf` with the rendered diff instead of
  raising `SnapshotAssertionError`.

## Development

```shell
make test              # go test -race ./...
make lint              # gofmt check + go vet
make format            # gofmt -w .
make update-snapshots  # re-record the example snapshots
```
