.PHONY: build run test lint fmt clean docker-build docker-up docker-down docker-logs docker-ps \
	tf-fmt tf-validate tf-plan-dev tf-apply-dev tf-destroy-dev tf-plan-prod tf-apply-prod

APP_NAME := relayer
BUILD_DIR := bin
COMPOSE := docker compose -f infra/docker/docker-compose.yml
TF_DIR := infra/terraform
TF_DEV := $(TF_DIR)/environments/dev
TF_PROD := $(TF_DIR)/environments/prod

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

tf-fmt:
	terraform -chdir=$(TF_DIR) fmt -recursive

tf-validate:
	terraform -chdir=$(TF_DEV) validate
	terraform -chdir=$(TF_PROD) validate

tf-plan-dev:
	terraform -chdir=$(TF_DEV) plan

tf-apply-dev:
	terraform -chdir=$(TF_DEV) apply

tf-destroy-dev:
	terraform -chdir=$(TF_DEV) destroy

tf-plan-prod:
	terraform -chdir=$(TF_PROD) plan

tf-apply-prod:
	terraform -chdir=$(TF_PROD) apply
