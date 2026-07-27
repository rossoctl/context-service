.PHONY: build test clean

build:
	go build -o bin/contextctl ./cmd/contextctl

test:
	go test ./...
	go vet ./...

clean:
	rm -rf bin
