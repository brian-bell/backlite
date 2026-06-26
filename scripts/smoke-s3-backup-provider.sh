#!/usr/bin/env bash
# Smoke-test Backlite S3 backup uploads against a real provider or S3-compatible endpoint.
set -euo pipefail

usage() {
    cat <<'USAGE'
Usage: smoke-s3-backup-provider.sh --bucket NAME [options]

Starts a temporary Backlite server against an isolated SQLite database, waits
for one local backup + S3 upload, validates the local artifact, checks the
remote object with AWS CLI, and verifies /debug/stats.

Required:
  --bucket NAME               S3 bucket name, or BACKFLOW_BACKUP_S3_BUCKET

Options:
  --prefix PREFIX             Object key prefix, or BACKFLOW_BACKUP_S3_PREFIX
  --region REGION             AWS/S3 region, or BACKFLOW_BACKUP_S3_REGION
  --endpoint-url URL          S3-compatible endpoint, or BACKFLOW_BACKUP_S3_ENDPOINT
  --path-style                Set BACKFLOW_BACKUP_S3_PATH_STYLE=true for Backlite
  --virtual-hosted-style      Set BACKFLOW_BACKUP_S3_PATH_STYLE=false for Backlite
  --setup-bucket              Run scripts/setup-backup-bucket.sh before smoke test
  --retention-days DAYS       Retention days passed to setup helper (default: 7)
  --port PORT                 Local Backlite port, or BACKFLOW_SMOKE_PORT
  --timeout SECONDS           Wait timeout, or BACKFLOW_SMOKE_TIMEOUT_SEC (default: 90)
  --keep-temp                 Keep temporary DB, backup dir, and logs after exit
  -h, --help                  Show this help

Prerequisites:
  aws, curl, go, gunzip, jq, sqlite3

AWS credentials must already be configured for the target provider. For an
S3-compatible provider, pass --endpoint-url and --path-style when required.
USAGE
}

die() {
    echo "error: $*" >&2
    exit 1
}

require_cmd() {
    command -v "$1" >/dev/null 2>&1 || die "$1 is required"
}

bucket="${BACKFLOW_BACKUP_S3_BUCKET:-}"
prefix="${BACKFLOW_BACKUP_S3_PREFIX:-}"
region="${BACKFLOW_BACKUP_S3_REGION:-${AWS_REGION:-${AWS_DEFAULT_REGION:-}}}"
endpoint="${BACKFLOW_BACKUP_S3_ENDPOINT:-}"
path_style="${BACKFLOW_BACKUP_S3_PATH_STYLE:-false}"
setup_bucket=0
retention_days=7
port="${BACKFLOW_SMOKE_PORT:-}"
timeout="${BACKFLOW_SMOKE_TIMEOUT_SEC:-90}"
backup_interval="${BACKFLOW_SMOKE_BACKUP_INTERVAL_SEC:-3600}"
keep_temp=0

while [[ $# -gt 0 ]]; do
    case "$1" in
        --bucket)
            bucket="${2:-}"
            shift 2
            ;;
        --prefix)
            prefix="${2:-}"
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
        --path-style)
            path_style=true
            shift
            ;;
        --virtual-hosted-style)
            path_style=false
            shift
            ;;
        --setup-bucket)
            setup_bucket=1
            shift
            ;;
        --retention-days)
            retention_days="${2:-}"
            shift 2
            ;;
        --port)
            port="${2:-}"
            shift 2
            ;;
        --timeout)
            timeout="${2:-}"
            shift 2
            ;;
        --keep-temp)
            keep_temp=1
            shift
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

[[ -n "$bucket" ]] || die "--bucket or BACKFLOW_BACKUP_S3_BUCKET is required"
[[ "$timeout" =~ ^[0-9]+$ ]] || die "--timeout must be a positive integer"
(( timeout > 0 )) || die "--timeout must be > 0"
[[ "$backup_interval" =~ ^[0-9]+$ ]] || die "BACKFLOW_SMOKE_BACKUP_INTERVAL_SEC must be a positive integer"
(( backup_interval > 0 )) || die "BACKFLOW_SMOKE_BACKUP_INTERVAL_SEC must be > 0"
[[ "$retention_days" =~ ^[0-9]+$ ]] || die "--retention-days must be a non-negative integer"
case "$path_style" in
    true|false) ;;
    *) die "BACKFLOW_BACKUP_S3_PATH_STYLE must be true or false" ;;
esac

for cmd in aws curl go gunzip jq sqlite3; do
    require_cmd "$cmd"
done

if [[ -z "$port" ]]; then
    if command -v python3 >/dev/null 2>&1; then
        port="$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1]); s.close()')"
    else
        port=18080
    fi
fi
[[ "$port" =~ ^[0-9]+$ ]] || die "--port must be a numeric TCP port"

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "$script_dir/.." && pwd)"
tmpdir="$(mktemp -d)"
backup_dir="$tmpdir/backups"
data_dir="$tmpdir/data"
db_path="$tmpdir/backlite-smoke.db"
log_file="$tmpdir/backlite.log"
binary="$tmpdir/backlite"
stats_file="$tmpdir/debug-stats.json"
restore_path="$tmpdir/restore.sqlite"
server_pid=""
base_url="http://127.0.0.1:$port"
seed_prompt="s3 backup manual smoke test https://github.com/brian-bell/backlite"

start_server() {
    local backup_enabled="$1"

    BACKFLOW_LISTEN_ADDR="127.0.0.1:$port" \
    BACKFLOW_DATABASE_PATH="$db_path" \
    BACKFLOW_DATA_DIR="$data_dir" \
    BACKFLOW_LOG_FILE="$log_file" \
    BACKFLOW_LOCAL_BACKUP_ENABLED="$backup_enabled" \
    BACKFLOW_LOCAL_BACKUP_DIR="$backup_dir" \
    BACKFLOW_LOCAL_BACKUP_INTERVAL_SEC="$backup_interval" \
    BACKFLOW_LOCAL_BACKUP_RETENTION_SEC=3600 \
    BACKFLOW_BACKUP_S3_BUCKET="$bucket" \
    BACKFLOW_BACKUP_S3_PREFIX="$prefix" \
    BACKFLOW_BACKUP_S3_REGION="$region" \
    BACKFLOW_BACKUP_S3_ENDPOINT="$endpoint" \
    BACKFLOW_BACKUP_S3_PATH_STYLE="$path_style" \
    BACKFLOW_MAX_CONTAINERS=0 \
    BACKFLOW_POLL_INTERVAL_SEC=1 \
    BACKFLOW_API_KEY= \
    ANTHROPIC_API_KEY="${ANTHROPIC_API_KEY:-sk-ant-smoke-placeholder-not-real}" \
    AWS_EC2_METADATA_DISABLED="${AWS_EC2_METADATA_DISABLED:-true}" \
        "$binary" >/dev/null 2>&1 &
    server_pid=$!
}

stop_server() {
    if [[ -n "$server_pid" ]] && kill -0 "$server_pid" >/dev/null 2>&1; then
        kill "$server_pid" >/dev/null 2>&1 || true
        wait "$server_pid" >/dev/null 2>&1 || true
    fi
    server_pid=""
}

wait_for_health() {
    for _ in $(seq 1 "$timeout"); do
        if ! kill -0 "$server_pid" >/dev/null 2>&1; then
            die "Backlite process exited before becoming healthy"
        fi
        if curl -sf "$base_url/health" >/dev/null; then
            return
        fi
        sleep 1
    done
    die "Backlite did not become healthy before timeout"
}

cleanup() {
    local status=$?
    stop_server

    if (( status != 0 )) && [[ -f "$log_file" ]]; then
        echo "--- backlite log ---" >&2
        tail -n 200 "$log_file" >&2 || true
    fi

    if (( keep_temp == 1 )); then
        echo "kept temporary smoke-test directory: $tmpdir" >&2
    else
        rm -rf "$tmpdir"
    fi
}
trap cleanup EXIT

aws_args=()
if [[ -n "$endpoint" ]]; then
    aws_args+=(--endpoint-url "$endpoint")
fi
if [[ -n "$region" ]]; then
    aws_args+=(--region "$region")
fi

aws_cmd() {
    aws "${aws_args[@]}" "$@"
}

normalized_prefix="$prefix"
while [[ "$normalized_prefix" == /* ]]; do
    normalized_prefix="${normalized_prefix#/}"
done
while [[ "$normalized_prefix" == */ ]]; do
    normalized_prefix="${normalized_prefix%/}"
done

if (( setup_bucket == 1 )); then
    setup_args=(--bucket "$bucket" --retention-days "$retention_days")
    if [[ -n "$prefix" ]]; then
        setup_args+=(--prefix "$prefix")
    fi
    if [[ -n "$region" ]]; then
        setup_args+=(--region "$region")
    fi
    if [[ -n "$endpoint" ]]; then
        setup_args+=(--endpoint-url "$endpoint")
    fi
    "$repo_root/scripts/setup-backup-bucket.sh" "${setup_args[@]}"
fi

echo "Building Backlite..."
(cd "$repo_root" && go build -trimpath -o "$binary" ./cmd/backlite)

mkdir -p "$backup_dir" "$data_dir"

echo "Starting isolated Backlite server on $base_url with backups disabled..."
start_server false
echo "Waiting for /health..."
wait_for_health

echo "Creating a pending task so the backup contains application data..."
payload="$(jq -n --arg prompt "$seed_prompt" '{prompt: $prompt, create_pr: false, save_agent_output: false}')"
create_response="$(curl -sf -X POST "$base_url/api/v1/tasks" \
    -H "Content-Type: application/json" \
    --data "$payload")"
task_id="$(jq -r '.data.id // empty' <<<"$create_response")"
[[ -n "$task_id" ]] || die "task creation response did not include data.id"
echo "Created task $task_id"

echo "Restarting Backlite with backup worker enabled..."
stop_server
start_server true
echo "Waiting for /health..."
wait_for_health

echo "Waiting for backup upload..."
deadline=$((SECONDS + timeout))
while (( SECONDS < deadline )); do
    if ! kill -0 "$server_pid" >/dev/null 2>&1; then
        die "Backlite process exited while waiting for backup upload"
    fi

    if curl -sf "$base_url/debug/stats" >"$stats_file"; then
        upload_enabled="$(jq -r 'if (.data.backup.upload_enabled? == true) then "true" else "false" end' "$stats_file")"
        pending_upload="$(jq -r 'if (.data.backup.pending_upload? == false) then "false" else "true" end' "$stats_file")"
        artifact_name="$(jq -r '.data.backup.latest_artifact.file_name // empty' "$stats_file")"
        uploaded_key="$(jq -r '.data.backup.latest_uploaded_artifact.key // empty' "$stats_file")"
        if [[ "$upload_enabled" == "true" && "$pending_upload" == "false" && -n "$artifact_name" && -n "$uploaded_key" ]]; then
            break
        fi
    fi
    sleep 1
done

[[ -s "$stats_file" ]] || die "debug stats were never written"
upload_enabled="$(jq -r 'if (.data.backup.upload_enabled? == true) then "true" else "false" end' "$stats_file")"
pending_upload="$(jq -r 'if (.data.backup.pending_upload? == false) then "false" else "true" end' "$stats_file")"
artifact_name="$(jq -r '.data.backup.latest_artifact.file_name // empty' "$stats_file")"
uploaded_bucket="$(jq -r '.data.backup.latest_uploaded_artifact.bucket // empty' "$stats_file")"
uploaded_key="$(jq -r '.data.backup.latest_uploaded_artifact.key // empty' "$stats_file")"
recent_upload_errors="$(jq '[.data.backup.recent_errors[]? | select(.phase == "upload")] | length' "$stats_file")"

[[ "$upload_enabled" == "true" ]] || die "/debug/stats backup.upload_enabled was not true"
[[ "$pending_upload" == "false" ]] || die "/debug/stats backup.pending_upload was not false"
[[ -n "$artifact_name" ]] || die "/debug/stats did not report latest_artifact.file_name"
[[ "$uploaded_bucket" == "$bucket" ]] || die "uploaded bucket $uploaded_bucket did not match $bucket"
[[ -n "$uploaded_key" ]] || die "/debug/stats did not report latest_uploaded_artifact.key"
[[ "$recent_upload_errors" == "0" ]] || die "/debug/stats reported upload errors"

artifact_path="$backup_dir/$artifact_name"
meta_path="$artifact_path.meta.json"
marker_path="$artifact_path.upload.json"
expected_key="$artifact_name"
if [[ -n "$normalized_prefix" ]]; then
    expected_key="$normalized_prefix/$artifact_name"
fi

[[ "$uploaded_key" == "$expected_key" ]] || die "uploaded key $uploaded_key did not match expected key $expected_key"
[[ -s "$artifact_path" ]] || die "local artifact missing: $artifact_path"
[[ -s "$meta_path" ]] || die "metadata sidecar missing: $meta_path"
[[ -s "$marker_path" ]] || die "upload marker missing: $marker_path"

meta_size="$(jq -r '.size_bytes' "$meta_path")"
meta_sha="$(jq -r '.sha256' "$meta_path")"
marker_bucket="$(jq -r '.bucket' "$marker_path")"
marker_key="$(jq -r '.key' "$marker_path")"
marker_endpoint="$(jq -r '.endpoint // ""' "$marker_path")"
marker_size="$(jq -r '.size_bytes' "$marker_path")"
marker_sha="$(jq -r '.sha256' "$marker_path")"

[[ "$marker_bucket" == "$bucket" ]] || die "marker bucket $marker_bucket did not match $bucket"
[[ "$marker_key" == "$expected_key" ]] || die "marker key $marker_key did not match $expected_key"
[[ "$marker_endpoint" == "$endpoint" ]] || die "marker endpoint $marker_endpoint did not match $endpoint"
[[ "$marker_size" == "$meta_size" ]] || die "marker size $marker_size did not match metadata size $meta_size"
[[ "$marker_sha" == "$meta_sha" ]] || die "marker sha $marker_sha did not match metadata sha $meta_sha"

echo "Checking remote object..."
head_json="$(aws_cmd s3api head-object --bucket "$bucket" --key "$expected_key" --output json)"
remote_size="$(jq -r '.ContentLength' <<<"$head_json")"
[[ "$remote_size" == "$marker_size" ]] || die "remote object size $remote_size did not match marker size $marker_size"

echo "Validating restored SQLite backup..."
gunzip -c "$artifact_path" >"$restore_path"
integrity="$(sqlite3 "$restore_path" "PRAGMA integrity_check;")"
[[ "$integrity" == "ok" ]] || die "restore integrity_check returned $integrity"
task_count="$(sqlite3 "$restore_path" "SELECT COUNT(*) FROM tasks;")"
(( task_count > 0 )) || die "restored database did not contain the seeded task"

echo "S3 backup provider smoke test passed."
echo "  bucket: $bucket"
echo "  key: $expected_key"
echo "  artifact: $artifact_path"
echo "  marker: $marker_path"
