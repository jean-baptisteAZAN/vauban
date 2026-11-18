VERSION ?= dev

.PHONY: build test
build:
	go build -ldflags "-X main.version=$(VERSION)" -o vauban .
test:
	go test ./...
