.PHONY: build run test clean docker-build docker-build-local docker-push docker-deploy lint \
       db-pending db-provisioning db-running db-completed db-failed db-interrupted db-cancelled db-recovering \
       setup-aws deps tunnel cloudflared-setup test-docker-status-writer copy-env

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
	go test ./... -v -count=1

test-docker-status-writer:
	bash scripts/test-docker-status-writer.sh

lint:
	go vet ./...

clean:
	rm -rf bin/

docker-build:
	$(DOCKER) buildx build \
		--platform linux/amd64,linux/arm64 \
		-t backflow-agent \
		docker/

docker-build-local:
	$(DOCKER) build -t backflow-agent docker/

docker-push:
	@echo "Usage: make docker-push REGISTRY=<ecr-uri>"
	$(DOCKER) tag backflow-agent $(REGISTRY):latest
	$(DOCKER) push $(REGISTRY):latest

docker-deploy:
	@$(ENV); \
	ACCOUNT_ID=$$(aws sts get-caller-identity --query Account --output text) && \
	REGION=$${AWS_REGION:-us-east-1} && \
	ECR=$$ACCOUNT_ID.dkr.ecr.$$REGION.amazonaws.com && \
	aws ecr get-login-password --region $$REGION | $(DOCKER) login --username AWS --password-stdin $$ECR && \
	$(DOCKER) buildx build \
		--platform linux/amd64,linux/arm64 \
		-t $$ECR/backflow-agent:latest \
		--push \
		docker/ && \
	echo "Pushed to $$ECR/backflow-agent:latest"

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

copy-env:
	cp ~/dev/etc/.env .env

deps:
	go mod tidy
