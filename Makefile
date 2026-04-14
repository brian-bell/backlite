.PHONY: build run test clean lint \
       docker-agent-build docker-agent-build-local docker-agent-push docker-agent-deploy \
       docker-server-build docker-server-build-local docker-server-deploy \
       docker-fake-agent-build test-fake-agent test-blackbox test-schema test-soak \
       db-pending db-provisioning db-running db-completed db-failed db-interrupted db-cancelled db-recovering \
       setup-aws deps tunnel cloudflared-setup test-docker-status-writer copy-env overwrite-env

BINARY := backflow
PKG := github.com/backflow-labs/backflow
GOFLAGS := -trimpath
export PATH := $(HOME)/.local/go/bin:$(HOME)/go/bin:$(PATH)

DOCKER ?= docker

# Helper to source .env before a command
ENV = if [ -f .env ]; then set -a; . ./.env; set +a; fi

build:
	go build $(GOFLAGS) -o bin/$(BINARY) ./cmd/backflow

run: build
	@set -e; \
	if [ -f .env ]; then set -a; . ./.env; set +a; fi; \
	if command -v aws >/dev/null 2>&1 && ! aws sts get-caller-identity >/dev/null 2>&1; then \
		echo "AWS credentials are missing or expired; running aws login"; \
		aws login; \
	fi; \
	./bin/$(BINARY)

test:
	go test -tags nocontainers ./... -v -count=1

test-docker-status-writer:
	bash scripts/test-docker-status-writer.sh

docker-fake-agent-build:
	$(DOCKER) build -t backflow-fake-agent test/blackbox/fake-agent/

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
		-t backflow-agent \
		docker/agent/

docker-agent-build-local:
	$(DOCKER) build -t backflow-agent docker/agent/

docker-agent-push:
	@echo "Usage: make docker-agent-push REGISTRY=<ecr-uri>"
	$(DOCKER) tag backflow-agent $(REGISTRY):latest
	$(DOCKER) push $(REGISTRY):latest

docker-agent-deploy:
	@$(ENV); \
	ACCOUNT_ID=$$(aws sts get-caller-identity --query Account --output text) && \
	REGION=$${AWS_REGION:-us-east-1} && \
	ECR=$$ACCOUNT_ID.dkr.ecr.$$REGION.amazonaws.com && \
	aws ecr get-login-password --region $$REGION | $(DOCKER) login --username AWS --password-stdin $$ECR && \
	$(DOCKER) buildx build \
		--platform linux/amd64,linux/arm64 \
		-t $$ECR/backflow-agent:latest \
		--push \
		docker/agent/ && \
	echo "Pushed to $$ECR/backflow-agent:latest"

docker-server-build:
	$(DOCKER) buildx build \
		--platform linux/amd64,linux/arm64 \
		-t backflow-server \
		-f docker/server/Dockerfile .

docker-server-build-local:
	$(DOCKER) build -t backflow-server -f docker/server/Dockerfile .

docker-server-deploy:
	@$(ENV); \
	ACCOUNT_ID=$$(aws sts get-caller-identity --query Account --output text) && \
	REGION=$${AWS_REGION:-us-east-1} && \
	ECR=$$ACCOUNT_ID.dkr.ecr.$$REGION.amazonaws.com && \
	aws ecr get-login-password --region $$REGION | $(DOCKER) login --username AWS --password-stdin $$ECR && \
	$(DOCKER) buildx build \
		--platform linux/amd64,linux/arm64 \
		-t $$ECR/backflow-server:latest \
		--push \
		-f docker/server/Dockerfile . && \
	echo "Pushed to $$ECR/backflow-server:latest"

DB_QUERY = @$(ENV); psql "$$BACKFLOW_DATABASE_URL" -c

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

setup-aws:
	@$(ENV); bash scripts/setup-aws.sh

tunnel:
	@$(ENV); \
	echo "Starting cloudflared tunnel → $$BACKFLOW_DOMAIN → http://localhost:8080"; \
	echo "Discord interactions endpoint: https://$$BACKFLOW_DOMAIN/webhooks/discord"; \
	echo "SMS inbound webhook:           https://$$BACKFLOW_DOMAIN/webhooks/sms/inbound"; \
	cloudflared tunnel run $$BACKFLOW_TUNNEL_NAME

cloudflared-setup:
	@$(ENV); bash scripts/cloudflared-setup.sh

restore-env:
	cp ~/dev/etc/backflow/.env .env

backup-env:
	cp .env ~/dev/etc/backflow/.env

deps:
	go mod tidy
