.PHONY: build run test clean docker-build docker-build-local docker-push docker-deploy lint db-status setup-aws deps

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
	@if [ -f .env ]; then set -a; . ./.env; set +a; fi; ./bin/$(BINARY)

test:
	go test ./... -v -count=1

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

db-status:
	@bash scripts/db-status.sh

setup-aws:
	@$(ENV); bash scripts/setup-aws.sh

deps:
	go mod tidy
