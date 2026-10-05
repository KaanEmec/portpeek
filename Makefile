.PHONY: test lint build

test:
	go vet ./...
	go test -race ./...

lint:
	test -z "$$(gofmt -l .)"
	golangci-lint run

build:
	go build -o portpeek ./cmd/portpeek
