.PHONY: build run test lint fmt clean docker-build docker-up docker-down docker-logs docker-ps

APP_NAME := relayer
BUILD_DIR := bin
COMPOSE := docker compose -f infra/docker/docker-compose.yml

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

docker-build:
	$(COMPOSE) build

docker-up:
	$(COMPOSE) up -d

docker-down:
	$(COMPOSE) down

docker-logs:
	$(COMPOSE) logs -f

docker-ps:
	$(COMPOSE) ps
