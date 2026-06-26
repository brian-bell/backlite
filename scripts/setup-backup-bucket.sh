#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'USAGE'
Usage: setup-backup-bucket.sh [--bucket NAME] [--region REGION] [--endpoint-url URL] [--prefix PREFIX] [--retention-days DAYS]

Creates or verifies an S3-compatible bucket for Backlite SQLite backup uploads.

Environment defaults:
  BACKFLOW_BACKUP_S3_BUCKET
  BACKFLOW_BACKUP_S3_REGION
  BACKFLOW_BACKUP_S3_ENDPOINT
  BACKFLOW_BACKUP_S3_PREFIX
  BACKFLOW_BACKUP_S3_RETENTION_DAYS
USAGE
}

bucket="${BACKFLOW_BACKUP_S3_BUCKET:-}"
region="${BACKFLOW_BACKUP_S3_REGION:-}"
endpoint="${BACKFLOW_BACKUP_S3_ENDPOINT:-}"
prefix="${BACKFLOW_BACKUP_S3_PREFIX:-}"
retention_days="${BACKFLOW_BACKUP_S3_RETENTION_DAYS:-30}"
created_bucket=0

while [[ $# -gt 0 ]]; do
  case "$1" in
    --bucket)
      bucket="${2:-}"
      shift 2
      ;;
    --region)
      region="${2:-}"
      shift 2
      ;;
    --endpoint-url|--endpoint)
      endpoint="${2:-}"
      shift 2
      ;;
    --prefix)
      prefix="${2:-}"
      shift 2
      ;;
    --retention-days)
      retention_days="${2:-}"
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "unknown argument: $1" >&2
      usage >&2
      exit 2
      ;;
  esac
done

if [[ -z "$bucket" ]]; then
  echo "BACKFLOW_BACKUP_S3_BUCKET or --bucket is required" >&2
  exit 2
fi

if ! [[ "$retention_days" =~ ^[0-9]+$ ]]; then
  echo "--retention-days must be a non-negative integer" >&2
  exit 2
fi

if [[ "$prefix" == *\"* || "$prefix" == *\\* ]]; then
  echo "--prefix must not contain double quotes or backslashes" >&2
  exit 2
fi

normalized_prefix="$prefix"
while [[ "$normalized_prefix" == /* ]]; do
  normalized_prefix="${normalized_prefix#/}"
done
while [[ "$normalized_prefix" == */ ]]; do
  normalized_prefix="${normalized_prefix%/}"
done
lifecycle_prefix=""
if [[ -n "$normalized_prefix" ]]; then
  lifecycle_prefix="${normalized_prefix}/"
fi

if ! command -v aws >/dev/null 2>&1; then
  echo "aws CLI is required; install AWS CLI v2 or a compatible provider CLI shim" >&2
  exit 127
fi

aws_args=()
if [[ -n "$endpoint" ]]; then
  aws_args+=(--endpoint-url "$endpoint")
fi

aws_cmd() {
  if (( ${#aws_args[@]} > 0 )); then
    aws "${aws_args[@]}" "$@"
  else
    aws "$@"
  fi
}

warn_optional() {
  local feature="$1"
  echo "warning: provider did not accept optional ${feature}; continuing because bucket is usable" >&2
}

if aws_cmd s3api head-bucket --bucket "$bucket" >/dev/null 2>&1; then
  echo "bucket exists: $bucket"
else
  create_args=(s3api create-bucket --bucket "$bucket")
  if [[ -n "$region" ]]; then
    create_args+=(--region "$region")
    if [[ "$region" != "us-east-1" ]]; then
      create_args+=(--create-bucket-configuration "LocationConstraint=$region")
    fi
  fi
  create_err="$(mktemp)"
  if ! aws_cmd "${create_args[@]}" >/dev/null 2>"$create_err"; then
    if [[ -n "$endpoint" && " ${create_args[*]} " == *" --create-bucket-configuration "* ]]; then
      warn_optional "bucket create location constraint"
      simple_create_args=(s3api create-bucket --bucket "$bucket")
      if [[ -n "$region" ]]; then
        simple_create_args+=(--region "$region")
      fi
      aws_cmd "${simple_create_args[@]}" >/dev/null
    else
      cat "$create_err" >&2
      rm -f "$create_err"
      exit 1
    fi
  fi
  rm -f "$create_err"
  echo "bucket created: $bucket"
  created_bucket=1
fi

if ! aws_cmd s3api put-public-access-block \
  --bucket "$bucket" \
  --public-access-block-configuration "BlockPublicAcls=true,IgnorePublicAcls=true,BlockPublicPolicy=true,RestrictPublicBuckets=true" >/dev/null 2>&1; then
  warn_optional "public-access block"
fi

if ! aws_cmd s3api put-bucket-encryption \
  --bucket "$bucket" \
  --server-side-encryption-configuration '{"Rules":[{"ApplyServerSideEncryptionByDefault":{"SSEAlgorithm":"AES256"}}]}' >/dev/null 2>&1; then
  warn_optional "server-side encryption"
fi

if (( retention_days > 0 && created_bucket == 1 )); then
  lifecycle_json=$(printf '{"Rules":[{"ID":"ExpireBackliteBackups","Status":"Enabled","Filter":{"Prefix":"%s"},"Expiration":{"Days":%d}}]}' "$lifecycle_prefix" "$retention_days")
  if ! aws_cmd s3api put-bucket-lifecycle-configuration \
    --bucket "$bucket" \
    --lifecycle-configuration "$lifecycle_json" >/dev/null 2>&1; then
    warn_optional "lifecycle retention"
  fi
elif (( retention_days > 0 )); then
  echo "warning: existing bucket lifecycle configuration was not changed; configure retention for prefix '${lifecycle_prefix}' manually if needed" >&2
fi

echo "backup bucket is ready: $bucket"
