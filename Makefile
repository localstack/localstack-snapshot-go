.PHONY: format lint test test-coverage update-snapshots

format:
	gofmt -w .

lint:
	@unformatted=$$(gofmt -l .); \
	if [ -n "$$unformatted" ]; then \
		echo "these files are not gofmt'ed:"; \
		echo "$$unformatted"; \
		exit 1; \
	fi
	go vet ./...

test:
	go test -race ./...

test-coverage:
	go test -race -coverprofile=coverage.out -covermode=atomic ./...
	go tool cover -func=coverage.out | tail -1

update-snapshots:
	UPDATE_SNAPSHOT=y go test ./...
