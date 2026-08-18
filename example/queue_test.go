// Package example_test shows how the snapshot library is used from a normal test package, with the
// snapshot file living next to the test file.
//
// Re-record it with:
//
//	UPDATE_SNAPSHOT=y go test ./example/
package example_test

import (
	"fmt"
	"math/rand"
	"testing"
	"time"

	"github.com/localstack/localstack-snapshot-go/snapshot"
)

// createQueue stands in for an API call: every call returns different ids and timestamps.
func createQueue(name string) map[string]any {
	id := fmt.Sprintf("%08x", rand.Uint32())
	url := fmt.Sprintf("http://localhost:4566/000000000000/%s-%s", name, id)

	return map[string]any{
		"QueueUrl": url,
		"Attributes": map[string]any{
			"QueueArn":                    fmt.Sprintf("arn:aws:sqs:us-east-1:000000000000:%s-%s", name, id),
			"CreatedTimestamp":            time.Now(),
			"ApproximateNumberOfMessages": 0,
			"Policy":                      `{"Version": "2012-10-17", "Statement": []}`,
		},
		"Tags": []any{
			map[string]any{"Key": "owner", "Value": "team-b"},
			map[string]any{"Key": "env", "Value": "test"},
		},
		"RequestId": id,
	}
}

func TestCreateQueue(t *testing.T) {
	snap := snapshot.New(t)
	snap.AddTransformer(
		// every occurrence of the queue url and arn becomes <queue-url:1> / <queue-arn:1>
		snapshot.Transform.KeyValue("QueueUrl", "queue-url", true),
		snapshot.Transform.KeyValue("QueueArn", "queue-arn", true),
		// timestamps become a fixed reference timestamp of the same format
		snapshot.Transform.Timestamp(),
		// tags come back in a random order
		snapshot.Transform.SortingByKey("Tags", "Key"),
	)
	// the request id is not worth asserting on
	snap.SkipVerifyPaths("$..RequestId")

	snap.Match("create-queue", createQueue("my-queue"))
}

func TestCreateQueueSubtests(t *testing.T) {
	for _, name := range []string{"first", "second"} {
		t.Run(name, func(t *testing.T) {
			// each subtest gets its own scope, keyed by the full test name
			snap := snapshot.New(t)
			snap.AddTransformer(
				snapshot.Transform.KeyValue("QueueUrl", "queue-url", true),
				snapshot.Transform.KeyValue("QueueArn", "queue-arn", true),
				snapshot.Transform.Timestamp(),
				snapshot.Transform.SortingByKey("Tags", "Key"),
			)
			snap.SkipVerifyPaths("$..RequestId")

			snap.Match("create-queue", createQueue(name))
		})
	}
}
