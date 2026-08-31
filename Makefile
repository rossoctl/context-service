.PHONY: build test clean kind-up kind-smoke kind-down

build:
	go build -o bin/contextctl ./cmd/contextctl

test:
	go test ./...
	go vet ./...

clean:
	rm -rf bin

kind-up: build
	./hack/kind-quickstart.sh up

kind-smoke: build
	./hack/kind-quickstart.sh smoke

kind-down:
	./hack/kind-quickstart.sh down
