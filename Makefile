.PHONY: build run test lint fmt clean

APP_NAME := relayer
BUILD_DIR := bin

build:
	go build -o $(BUILD_DIR)/$(APP_NAME) ./cmd/relayer

run:
	go run ./cmd/relayer

test:
	go test ./... -v

lint:
	golangci-lint run ./...

fmt:
	gofmt -w .

clean:
	rm -rf $(BUILD_DIR)
