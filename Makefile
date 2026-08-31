.PHONY: build test clean kind-up kind-demo kind-demo-clean kind-smoke kind-down

build:
	go build -o bin/contextctl ./cmd/contextctl

test:
	go test ./...
	go vet ./...

clean:
	rm -rf bin

kind-up: build
	./hack/kind-quickstart.sh up

kind-demo: kind-up
	./hack/kind-quickstart.sh demo

kind-demo-clean: build
	./hack/kind-quickstart.sh demo-clean

kind-smoke: build
	./hack/kind-quickstart.sh smoke

kind-down:
	./hack/kind-quickstart.sh down
