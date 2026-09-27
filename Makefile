.PHONY: build run test test-e2e cover lint fmt clean loadtest \
	docker-build docker-up docker-down docker-logs docker-ps tunnel publish-check publish \
	tf-fmt tf-validate tf-plan-dev tf-apply-dev tf-destroy-dev tf-plan-prod tf-apply-prod

# Machine-specific settings (PI, RELAYER_URL, RELAYER_API_KEY_FILE) live in the gitignored
# local.mk; see local.mk.example.
-include local.mk

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

# End-to-end suite. Redis-backed cases self-skip when no Redis is reachable;
# run `make docker-up` (or a local redis) first to exercise the full flow.
test-e2e:
	go test ./test/e2e/... -v

cover:
	go test ./... -coverprofile=coverage.out
	go tool cover -func=coverage.out | tail -1

lint:
	golangci-lint run ./...

fmt:
	gofmt -w .

clean:
	rm -rf $(BUILD_DIR) coverage.out

# Load test against a running instance. Override URL/API key as needed:
#   make loadtest URL=http://localhost:8080 API_KEY=<key>
URL ?= http://localhost:8080
loadtest:
	@test -n "$(API_KEY)" || { echo "usage: make loadtest URL=... API_KEY=<key>"; exit 1; }
	./scripts/loadtest.sh $(URL) '$(API_KEY)'

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

# Free ephemeral Cloudflare Tunnel exposing the relayer publicly over HTTPS.
# Requires `cloudflared` installed. Run `make docker-up` first.
tunnel:
	cloudflared tunnel --url http://localhost:8080

# Deploy to the Raspberry Pi over Tailscale (rsync + compose build on the Pi).
# Ports bind to the Pi's tailnet IP only. Set GRAFANA_PASSWORD in the Pi's ~/relayer/infra/docker/.env.
sync-pi:
	@test -n "$(PI)" || { echo "set PI=<user>@<pi-host> (e.g. in local.mk)"; exit 1; }
	rsync -az --delete --exclude .git --exclude bin --exclude infra/terraform --exclude private \
		--filter='P configs/config.yaml' --filter='P infra/docker/.env' --filter='P infra/docker/.env.relayer' --include configs/config.yaml --filter=':- .gitignore' ./ $(PI):relayer/

deploy-pi: sync-pi
	# restart: config.yaml is a bind mount, so a config-only change does not recreate the container
	ssh $(PI) 'cd relayer/infra/docker && BIND_IP=$$(tailscale ip -4) docker compose up -d --build && docker compose restart relayer'

# Generate an API key and the config line for it. Hand the key to the client; put only the hash in config.
new-key:
	@k=$$(openssl rand -hex 32); echo "API key (give to the client, do not store it): $$k"; \
	echo "config:  key_sha256: \"$$(printf %s $$k | shasum -a 256 | cut -d' ' -f1)\""

# Mixed synthetic traffic so the Grafana dashboards move (Ctrl-C to stop). make traffic ARGS=--burst
# Uses RELAYER_URL and RELAYER_API_KEY(_FILE) from the environment or local.mk.
traffic:
	./scripts/traffic.sh $(ARGS)

# Public mirror: build a scrubbed export of main and verify it (publish-check), then push it
# (publish). Settings in local.mk: PUBLISH_REPLACEMENTS, PUBLISH_MAILMAP, PUBLIC_REMOTE.
publish-check:
	./scripts/publish.sh

publish:
	./scripts/publish.sh --push

# Isolated throwaway stack on the Pi; results land in benchmarks/results/.
bench:
	PI=$(PI) ./benchmarks/run.sh

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
