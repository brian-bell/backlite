.PHONY: build run test clean lint \
       web-deps web-generate web-dev web-build web-test \
       docker-agent-build-local \
       docker-reader-build-local \
       docker-skill-agent-build-local \
       docker-agents-build-local \
       test-fake-agent test-blackbox test-schema test-soak \
       test-skill-agent-entrypoint \
       test-reader-fetch-extract \
       db-pending db-running db-completed db-failed \
       deps

BINARY := backlite
GOFLAGS := -trimpath
export PATH := $(HOME)/.local/go/bin:$(HOME)/go/bin:$(PATH)

DOCKER ?= docker

# Helper to source .env before a command
ENV = if [ -f .env ]; then set -a; . ./.env; set +a; fi

build: web-build
	go build $(GOFLAGS) -o bin/$(BINARY) ./cmd/backlite

run: build
	@set -e; \
	if [ -f .env ]; then set -a; . ./.env; set +a; fi; \
	./bin/$(BINARY)

test:
	go test -tags nocontainers ./... -v -count=1

web-deps:
	cd web && npm install

web-generate:
	cd web && npm run generate:api

web-dev:
	cd web && npm run dev

web-build: web-generate
	cd web && npm run build

web-test:
	cd web && npm test

test-fake-agent:
	$(DOCKER) build -t backlite-fake-agent test/blackbox/fake-agent/
	go test ./test/blackbox/fake-agent/ -v -count=1

test-blackbox:
	bash scripts/test-blackbox.sh

test-soak:
	bash scripts/test-soak.sh --short

test-schema:
	bash scripts/test-schema.sh

lint:
	go vet ./...

clean:
	rm -rf bin/

docker-agent-build-local:
	$(DOCKER) build -t backlite-agent docker/agent/

docker-reader-build-local:
	$(DOCKER) build -t backlite-reader docker/reader/

docker-skill-agent-build-local:
	$(DOCKER) build -t backlite-skill-agent docker/skill-agent/

docker-agents-build-local: docker-agent-build-local docker-reader-build-local docker-skill-agent-build-local

test-skill-agent-entrypoint:
	bash docker/skill-agent/test_entrypoint.sh

test-reader-fetch-extract:
	bash docker/reader/test_fetch_and_extract.sh

DB_QUERY = @$(ENV); sqlite3 -json "$$BACKFLOW_DATABASE_PATH"

db-pending:
	$(DB_QUERY) "SELECT id, repo_url, branch, harness, created_at FROM tasks WHERE status = 'pending' ORDER BY created_at ASC;"

db-running:
	$(DB_QUERY) "SELECT id, repo_url, branch, harness, model, started_at, elapsed_time_sec FROM tasks WHERE status = 'running' ORDER BY started_at ASC;"

db-completed:
	$(DB_QUERY) "SELECT id, repo_url, branch, harness, pr_url, cost_usd, elapsed_time_sec, completed_at FROM tasks WHERE status = 'completed' ORDER BY completed_at DESC;"

db-failed:
	$(DB_QUERY) "SELECT id, repo_url, branch, harness, error, completed_at FROM tasks WHERE status = 'failed' ORDER BY completed_at DESC;"

deps:
	go mod tidy
