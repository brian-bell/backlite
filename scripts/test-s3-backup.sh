#!/usr/bin/env bash
# Run the S3-compatible backup upload integration test against a local MinIO container.
set -euo pipefail

usage() {
    cat <<EOF
Usage: $(basename "$0") [go test flags]

Starts a temporary MinIO container, runs the backup manager's real AWS SDK
upload path against it, and removes the container on exit.

Environment variables:
  MINIO_IMAGE                    Container image (default: minio/minio:latest)
  BACKFLOW_TEST_S3_ACCESS_KEY    MinIO root user (default: minioadmin)
  BACKFLOW_TEST_S3_SECRET_KEY    MinIO root password (default: minioadmin)
  BACKFLOW_TEST_S3_REGION        Test region (default: us-east-1)

Examples:
  $(basename "$0")
  $(basename "$0") -count=1
EOF
}

case "${1:-}" in
    -h|--help) usage; exit 0 ;;
esac

if ! docker info >/dev/null 2>&1; then
    echo "error: Docker daemon is not running" >&2
    exit 1
fi

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

image="${MINIO_IMAGE:-minio/minio:latest}"
access_key="${BACKFLOW_TEST_S3_ACCESS_KEY:-minioadmin}"
secret_key="${BACKFLOW_TEST_S3_SECRET_KEY:-minioadmin}"
region="${BACKFLOW_TEST_S3_REGION:-us-east-1}"
container="backlite-minio-s3-test-$$-$(date +%s)"

cleanup() {
    local status=$?
    if (( status != 0 )); then
        docker logs "$container" >&2 || true
    fi
    docker rm -f "$container" >/dev/null 2>&1 || true
    exit "$status"
}
trap cleanup EXIT

echo "Starting MinIO container $container..."
docker run -d \
    --name "$container" \
    -e "MINIO_ROOT_USER=$access_key" \
    -e "MINIO_ROOT_PASSWORD=$secret_key" \
    -p 127.0.0.1::9000 \
    "$image" server /data >/dev/null

port="$(docker port "$container" 9000/tcp | head -n1 | awk -F: '{print $NF}')"
if [[ -z "$port" ]]; then
    echo "error: could not resolve MinIO host port" >&2
    exit 1
fi
endpoint="http://127.0.0.1:$port"

echo "Waiting for MinIO at $endpoint..."
if command -v curl >/dev/null 2>&1; then
    for _ in $(seq 1 60); do
        curl -sf "$endpoint/minio/health/ready" >/dev/null && break
        sleep 0.5
    done
fi

echo "Running S3 backup integration test..."
cd "$ROOT_DIR"
BACKFLOW_TEST_S3_ENDPOINT="$endpoint" \
BACKFLOW_TEST_S3_ACCESS_KEY="$access_key" \
BACKFLOW_TEST_S3_SECRET_KEY="$secret_key" \
BACKFLOW_TEST_S3_REGION="$region" \
    go test -tags s3integration ./internal/backup \
        -run TestS3BackupUploadEndToEndWithMinIO \
        -count=1 \
        -v \
        "$@"
