#!/usr/bin/env bash
set -euo pipefail

# Build and push the backlite agent Docker image (multi-arch)

REGION="${AWS_REGION:-us-east-1}"
ECR_REPO="backlite-agent"

ACCOUNT_ID=$(aws sts get-caller-identity --query Account --output text)
ECR_URI="${ACCOUNT_ID}.dkr.ecr.${REGION}.amazonaws.com/${ECR_REPO}"

echo "==> Authenticating with ECR..."
aws ecr get-login-password --region "$REGION" | docker login --username AWS --password-stdin "${ACCOUNT_ID}.dkr.ecr.${REGION}.amazonaws.com"

echo "==> Building multi-arch image..."
docker buildx create --name backlite-builder --use 2>/dev/null || docker buildx use backlite-builder

docker buildx build \
    --platform linux/amd64,linux/arm64 \
    -t "${ECR_URI}:latest" \
    --push \
    docker/agent/

echo "==> Pushed to ${ECR_URI}:latest"
