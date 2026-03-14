.PHONY: build run test clean docker-build docker-push lint

BINARY := backflow
PKG := github.com/backflow-labs/backflow
GOFLAGS := -trimpath
export PATH := $(HOME)/.local/go/bin:$(HOME)/go/bin:$(PATH)

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
	docker buildx build \
		--platform linux/amd64,linux/arm64 \
		-t backflow-agent \
		docker/

docker-build-local:
	docker build -t backflow-agent docker/

docker-push:
	@echo "Usage: make docker-push REGISTRY=<ecr-uri>"
	docker tag backflow-agent $(REGISTRY):latest
	docker push $(REGISTRY):latest

setup-aws:
	bash scripts/setup-aws.sh

deps:
	go mod tidy
