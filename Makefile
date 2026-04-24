.PHONY: build run test clean lint \
       docker-agent-build docker-agent-build-local \
       docker-reader-build docker-reader-build-local \
       docker-server-build docker-server-build-local \
       docker-fake-agent-build test-fake-agent test-blackbox test-schema test-soak \
       db-pending db-provisioning db-running db-completed db-failed db-interrupted db-cancelled db-recovering \
       teardown-aws deps test-docker-status-writer test-reader-status-writer \
       test-reader-scripts

BINARY := backlite
PKG := github.com/brian-bell/backlite
GOFLAGS := -trimpath
export PATH := $(HOME)/.local/go/bin:$(HOME)/go/bin:$(PATH)

DOCKER ?= docker

# Helper to source .env before a command
ENV = if [ -f .env ]; then set -a; . ./.env; set +a; fi

build:
	go build $(GOFLAGS) -o bin/$(BINARY) ./cmd/backlite

run: build
	@set -e; \
	if [ -f .env ]; then set -a; . ./.env; set +a; fi; \
	./bin/$(BINARY)

test:
	go test -tags nocontainers ./... -v -count=1

test-docker-status-writer:
	bash scripts/test-docker-status-writer.sh

test-reader-status-writer:
	bash scripts/test-reader-status-writer.sh

test-reader-scripts:
	bash docker/reader/test_read_scripts.sh
	bash docker/reader/test_entrypoint.sh

docker-fake-agent-build:
	$(DOCKER) build -t backlite-fake-agent test/blackbox/fake-agent/

test-fake-agent: docker-fake-agent-build
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

docker-agent-build:
	$(DOCKER) buildx build \
		--platform linux/amd64,linux/arm64 \
		-t backlite-agent \
		docker/agent/

docker-agent-build-local:
	$(DOCKER) build -t backlite-agent docker/agent/

docker-reader-build:
	$(DOCKER) buildx build \
		--platform linux/amd64,linux/arm64 \
		-t backlite-reader \
		docker/reader/

docker-reader-build-local:
	$(DOCKER) build -t backlite-reader docker/reader/

docker-server-build:
	$(DOCKER) buildx build \
		--platform linux/amd64,linux/arm64 \
		-t backlite-server \
		-f docker/server/Dockerfile .

docker-server-build-local:
	$(DOCKER) build -t backlite-server -f docker/server/Dockerfile .

DB_QUERY = @$(ENV); sqlite3 -json "$$BACKFLOW_DATABASE_PATH"

db-pending:
	$(DB_QUERY) "SELECT id, repo_url, branch, harness, created_at FROM tasks WHERE status = 'pending' ORDER BY created_at ASC;"

db-provisioning:
	$(DB_QUERY) "SELECT id, repo_url, branch, harness, instance_id, created_at FROM tasks WHERE status = 'provisioning' ORDER BY created_at ASC;"

db-running:
	$(DB_QUERY) "SELECT id, repo_url, branch, harness, model, instance_id, started_at, elapsed_time_sec FROM tasks WHERE status = 'running' ORDER BY started_at ASC;"

db-completed:
	$(DB_QUERY) "SELECT id, repo_url, branch, harness, pr_url, cost_usd, elapsed_time_sec, completed_at FROM tasks WHERE status = 'completed' ORDER BY completed_at DESC;"

db-failed:
	$(DB_QUERY) "SELECT id, repo_url, branch, harness, error, completed_at FROM tasks WHERE status = 'failed' ORDER BY completed_at DESC;"

db-interrupted:
	$(DB_QUERY) "SELECT id, repo_url, branch, harness, error, retry_count, updated_at FROM tasks WHERE status = 'interrupted' ORDER BY updated_at DESC;"

db-cancelled:
	$(DB_QUERY) "SELECT id, repo_url, branch, harness, completed_at FROM tasks WHERE status = 'cancelled' ORDER BY completed_at DESC;"

db-recovering:
	$(DB_QUERY) "SELECT id, repo_url, branch, harness, instance_id, container_id, updated_at FROM tasks WHERE status = 'recovering' ORDER BY updated_at ASC;"

teardown-aws:
	@bash scripts/teardown-aws.sh $(ARGS)

deps:
	go mod tidy
