#!/usr/bin/env bash
# Smoke-test Backlite S3 backup uploads against a real provider or S3-compatible endpoint.
set -euo pipefail

usage() {
    cat <<'USAGE'
Usage: smoke-s3-backup-provider.sh --bucket NAME [options]

Runs one or more manual S3-backup smoke-test phases against a real provider or
S3-compatible endpoint. By default, it runs phase 1 only.

Required:
  --bucket NAME               S3 bucket name, or BACKFLOW_BACKUP_S3_BUCKET

Options:
  --phase PHASE               Phase to run: 1,2,3,4,5, or all (repeatable;
                              comma-separated values accepted; default: 1)
  --prefix PREFIX             Object key prefix, or BACKFLOW_BACKUP_S3_PREFIX
  --region REGION             AWS/S3 region, or BACKFLOW_BACKUP_S3_REGION
  --endpoint-url URL          S3-compatible endpoint, or BACKFLOW_BACKUP_S3_ENDPOINT
  --path-style                Set BACKFLOW_BACKUP_S3_PATH_STYLE=true for Backlite
  --virtual-hosted-style      Set BACKFLOW_BACKUP_S3_PATH_STYLE=false for Backlite
  --setup-bucket              Run scripts/setup-backup-bucket.sh before smoke test
  --retention-days DAYS       Retention days passed to setup helper (default: 7)
  --failure-aws-profile NAME  AWS profile with denied PutObject for phase 3
  --recovery-aws-profile NAME AWS profile with valid PutObject for phase 4
  --restore-artifact PATH     Existing .sqlite.gz artifact for phase 5
  --phase3-watch-sec SECONDS  Seconds to watch for duplicate artifacts after
                              phase 3 upload failure (default: 5)
  --port PORT                 Local Backlite port, or BACKFLOW_SMOKE_PORT
  --timeout SECONDS           Wait timeout, or BACKFLOW_SMOKE_TIMEOUT_SEC (default: 90)
  --keep-temp                 Keep temporary DB, backup dir, and logs after exit
  -h, --help                  Show this help

Phases:
  Phase 1: upload a seeded SQLite backup and verify local, remote, and stats
  Phase 2: verify setup helper does not overwrite existing bucket lifecycle
  Phase 3: verify upload permission failure preserves local backup state
  Phase 4: recover from phase 3 by uploading the existing local artifact
  Phase 5: restore a validated artifact and restart Backlite against it

Prerequisites:
  aws, curl, go, gunzip, jq, sqlite3

AWS credentials must already be configured for the target provider. For an
S3-compatible provider, pass --endpoint-url and --path-style when required.
Phase 3 must run with credentials that cannot put objects to the bucket/prefix;
pass --failure-aws-profile when your default credentials are valid.
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
phases=()
failure_aws_profile="${BACKFLOW_SMOKE_FAILURE_AWS_PROFILE:-}"
recovery_aws_profile="${BACKFLOW_SMOKE_RECOVERY_AWS_PROFILE:-}"
restore_artifact="${BACKFLOW_SMOKE_RESTORE_ARTIFACT:-}"
phase3_watch_sec="${BACKFLOW_SMOKE_PHASE3_WATCH_SEC:-5}"
port="${BACKFLOW_SMOKE_PORT:-}"
timeout="${BACKFLOW_SMOKE_TIMEOUT_SEC:-90}"
backup_interval="${BACKFLOW_SMOKE_BACKUP_INTERVAL_SEC:-3600}"
keep_temp=0

add_phase() {
    local spec="$1"
    local entry
    local old_ifs="$IFS"
    IFS=,
    for entry in $spec; do
        case "$entry" in
            all)
                phases=(1 2 3 4 5)
                ;;
            1|2|3|4|5)
                phases+=("$entry")
                ;;
            *)
                die "invalid phase '$entry'; expected 1,2,3,4,5, or all"
                ;;
        esac
    done
    IFS="$old_ifs"
}

while [[ $# -gt 0 ]]; do
    case "$1" in
        --phase|--phases)
            add_phase "${2:-}"
            shift 2
            ;;
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
        --failure-aws-profile|--restricted-aws-profile)
            failure_aws_profile="${2:-}"
            shift 2
            ;;
        --recovery-aws-profile|--valid-aws-profile)
            recovery_aws_profile="${2:-}"
            shift 2
            ;;
        --restore-artifact)
            restore_artifact="${2:-}"
            shift 2
            ;;
        --phase3-watch-sec)
            phase3_watch_sec="${2:-}"
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
if (( ${#phases[@]} == 0 )); then
    phases=(1)
fi
[[ "$timeout" =~ ^[0-9]+$ ]] || die "--timeout must be a positive integer"
(( timeout > 0 )) || die "--timeout must be > 0"
[[ "$backup_interval" =~ ^[0-9]+$ ]] || die "BACKFLOW_SMOKE_BACKUP_INTERVAL_SEC must be a positive integer"
(( backup_interval > 0 )) || die "BACKFLOW_SMOKE_BACKUP_INTERVAL_SEC must be > 0"
[[ "$retention_days" =~ ^[0-9]+$ ]] || die "--retention-days must be a non-negative integer"
[[ "$phase3_watch_sec" =~ ^[0-9]+$ ]] || die "--phase3-watch-sec must be a non-negative integer"
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
    local aws_profile="${2:-}"

    (
        if [[ -n "$aws_profile" ]]; then
            unset AWS_ACCESS_KEY_ID AWS_SECRET_ACCESS_KEY AWS_SESSION_TOKEN
            export AWS_PROFILE="$aws_profile"
        fi
        export BACKFLOW_LISTEN_ADDR="127.0.0.1:$port"
        export BACKFLOW_DATABASE_PATH="$db_path"
        export BACKFLOW_DATA_DIR="$data_dir"
        export BACKFLOW_LOG_FILE="$log_file"
        export BACKFLOW_LOCAL_BACKUP_ENABLED="$backup_enabled"
        export BACKFLOW_LOCAL_BACKUP_DIR="$backup_dir"
        export BACKFLOW_LOCAL_BACKUP_INTERVAL_SEC="$backup_interval"
        export BACKFLOW_LOCAL_BACKUP_RETENTION_SEC=3600
        export BACKFLOW_BACKUP_S3_BUCKET="$bucket"
        export BACKFLOW_BACKUP_S3_PREFIX="$prefix"
        export BACKFLOW_BACKUP_S3_REGION="$region"
        export BACKFLOW_BACKUP_S3_ENDPOINT="$endpoint"
        export BACKFLOW_BACKUP_S3_PATH_STYLE="$path_style"
        export BACKFLOW_MAX_CONTAINERS=0
        export BACKFLOW_POLL_INTERVAL_SEC=1
        export BACKFLOW_API_KEY=
        export ANTHROPIC_API_KEY="${ANTHROPIC_API_KEY:-sk-ant-smoke-placeholder-not-real}"
        export AWS_EC2_METADATA_DISABLED="${AWS_EC2_METADATA_DISABLED:-true}"
        exec "$binary"
    ) >/dev/null 2>&1 &
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

setup_bucket_if_requested() {
    if (( setup_bucket != 1 )); then
        return 0
    fi

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
}

backlite_built=0
task_id=""
artifact_name=""
artifact_path=""
meta_path=""
marker_path=""
expected_key=""

build_backlite() {
    if (( backlite_built == 0 )); then
        echo "Building Backlite..."
        (cd "$repo_root" && go build -trimpath -o "$binary" ./cmd/backlite)
        backlite_built=1
    fi
    mkdir -p "$backup_dir" "$data_dir"
}

artifact_count() {
    find "$backup_dir" -maxdepth 1 -type f -name 'backlite-*.sqlite.gz' 2>/dev/null | wc -l | tr -d ' '
}

set_artifact_paths() {
    artifact_name="$1"
    artifact_path="$backup_dir/$artifact_name"
    meta_path="$artifact_path.meta.json"
    marker_path="$artifact_path.upload.json"
    expected_key="$artifact_name"
    if [[ -n "$normalized_prefix" ]]; then
        expected_key="$normalized_prefix/$artifact_name"
    fi
}

refresh_latest_from_stats() {
    [[ -s "$stats_file" ]] || die "debug stats were never written"
    artifact_name="$(jq -r '.data.backup.latest_artifact.file_name // empty' "$stats_file")"
    [[ -n "$artifact_name" ]] || die "/debug/stats did not report latest_artifact.file_name"
    set_artifact_paths "$artifact_name"
}

create_seed_task() {
    if [[ -n "$task_id" ]]; then
        return 0
    fi

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
    stop_server
}

wait_for_upload_success() {
    echo "Waiting for backup upload..."
    deadline=$((SECONDS + timeout))
    while (( SECONDS < deadline )); do
        if ! kill -0 "$server_pid" >/dev/null 2>&1; then
            die "Backlite process exited while waiting for backup upload"
        fi

        if curl -sf "$base_url/debug/stats" >"$stats_file"; then
            upload_enabled="$(jq -r 'if (.data.backup.upload_enabled? == true) then "true" else "false" end' "$stats_file")"
            pending_upload="$(jq -r 'if (.data.backup.pending_upload? == false) then "false" else "true" end' "$stats_file")"
            latest_name="$(jq -r '.data.backup.latest_artifact.file_name // empty' "$stats_file")"
            uploaded_key="$(jq -r '.data.backup.latest_uploaded_artifact.key // empty' "$stats_file")"
            if [[ "$upload_enabled" == "true" && "$pending_upload" == "false" && -n "$latest_name" && -n "$uploaded_key" ]]; then
                return
            fi
        fi
        sleep 1
    done
    die "backup upload did not complete before timeout"
}

validate_uploaded_artifact() {
    local allow_upload_errors="${1:-false}"

    [[ -s "$stats_file" ]] || die "debug stats were never written"
    upload_enabled="$(jq -r 'if (.data.backup.upload_enabled? == true) then "true" else "false" end' "$stats_file")"
    pending_upload="$(jq -r 'if (.data.backup.pending_upload? == false) then "false" else "true" end' "$stats_file")"
    uploaded_bucket="$(jq -r '.data.backup.latest_uploaded_artifact.bucket // empty' "$stats_file")"
    uploaded_key="$(jq -r '.data.backup.latest_uploaded_artifact.key // empty' "$stats_file")"
    recent_upload_errors="$(jq '[.data.backup.recent_errors[]? | select(.phase == "upload")] | length' "$stats_file")"

    [[ "$upload_enabled" == "true" ]] || die "/debug/stats backup.upload_enabled was not true"
    [[ "$pending_upload" == "false" ]] || die "/debug/stats backup.pending_upload was not false"
    refresh_latest_from_stats
    [[ "$uploaded_bucket" == "$bucket" ]] || die "uploaded bucket $uploaded_bucket did not match $bucket"
    [[ -n "$uploaded_key" ]] || die "/debug/stats did not report latest_uploaded_artifact.key"
    if [[ "$allow_upload_errors" != "true" ]]; then
        [[ "$recent_upload_errors" == "0" ]] || die "/debug/stats reported upload errors"
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
}

run_phase_1() {
    echo "Phase 1: real provider upload smoke"
    build_backlite
    create_seed_task

    echo "Starting Backlite with backup worker enabled..."
    start_server true "$recovery_aws_profile"
    echo "Waiting for /health..."
    wait_for_health
    wait_for_upload_success
    validate_uploaded_artifact false
    stop_server

    echo "S3 backup provider smoke test passed."
    echo "  bucket: $bucket"
    echo "  key: $expected_key"
    echo "  artifact: $artifact_path"
    echo "  marker: $marker_path"
    echo "Phase 1 passed"
}

capture_lifecycle() {
    local output_path="$1"
    local error_path="$2"
    if aws_cmd s3api get-bucket-lifecycle-configuration --bucket "$bucket" --output json >"$output_path" 2>"$error_path"; then
        jq -S . "$output_path" >"$output_path.normalized"
        echo "present"
    else
        : >"$output_path.normalized"
        echo "absent"
    fi
}

run_phase_2() {
    echo "Phase 2: existing bucket lifecycle safety"
    (( retention_days > 0 )) || die "phase 2 requires --retention-days > 0"
    aws_cmd s3api head-bucket --bucket "$bucket" >/dev/null || die "phase 2 requires an existing bucket; create it first or pass --setup-bucket"

    before_status="$(capture_lifecycle "$tmpdir/lifecycle-before.json" "$tmpdir/lifecycle-before.err")"

    phase2_stdout="$tmpdir/phase2-setup.out"
    phase2_stderr="$tmpdir/phase2-setup.err"
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
    if ! "$repo_root/scripts/setup-backup-bucket.sh" "${setup_args[@]}" >"$phase2_stdout" 2>"$phase2_stderr"; then
        cat "$phase2_stdout"
        cat "$phase2_stderr" >&2
        die "setup helper failed during phase 2"
    fi
    cat "$phase2_stdout"
    cat "$phase2_stderr" >&2

    if ! grep -F -- "existing bucket lifecycle configuration was not changed" "$phase2_stderr" >/dev/null; then
        die "setup helper did not report existing-bucket lifecycle safety warning"
    fi

    after_status="$(capture_lifecycle "$tmpdir/lifecycle-after.json" "$tmpdir/lifecycle-after.err")"
    [[ "$after_status" == "$before_status" ]] || die "bucket lifecycle status changed from $before_status to $after_status"
    if [[ "$before_status" == "present" ]]; then
        cmp -s "$tmpdir/lifecycle-before.json.normalized" "$tmpdir/lifecycle-after.json.normalized" || die "existing bucket lifecycle configuration changed"
    fi

    echo "Phase 2 passed"
}

wait_for_upload_failure() {
    echo "Waiting for expected backup upload failure..."
    deadline=$((SECONDS + timeout))
    while (( SECONDS < deadline )); do
        if ! kill -0 "$server_pid" >/dev/null 2>&1; then
            die "Backlite process exited while waiting for expected upload failure"
        fi

        if curl -sf "$base_url/debug/stats" >"$stats_file"; then
            latest_name="$(jq -r '.data.backup.latest_artifact.file_name // empty' "$stats_file")"
            pending_upload="$(jq -r 'if (.data.backup.pending_upload? == true) then "true" else "false" end' "$stats_file")"
            next_attempt="$(jq -r '.data.backup.next_upload_attempt_at // empty' "$stats_file")"
            upload_errors="$(jq '[.data.backup.recent_errors[]? | select(.phase == "upload")] | length' "$stats_file")"
            uploaded_key="$(jq -r '.data.backup.latest_uploaded_artifact.key // empty' "$stats_file")"
            if [[ -n "$uploaded_key" ]]; then
                die "phase 3 expected upload failure, but upload succeeded; use credentials without PutObject permission"
            fi
            if [[ -n "$latest_name" && "$pending_upload" == "true" && -n "$next_attempt" && "$upload_errors" != "0" ]]; then
                return
            fi
        fi
        sleep 1
    done
    die "expected upload failure was not observed before timeout"
}

run_phase_3() {
    echo "Phase 3: permission failure behavior"
    build_backlite
    create_seed_task
    stop_server

    if [[ -n "$marker_path" && -f "$marker_path" ]]; then
        rm -f "$marker_path"
    fi
    before_count="$(artifact_count)"

    echo "Starting Backlite with backup worker enabled under restricted credentials..."
    start_server true "$failure_aws_profile"
    echo "Waiting for /health..."
    wait_for_health
    wait_for_upload_failure
    refresh_latest_from_stats

    [[ -s "$artifact_path" ]] || die "local artifact missing after upload failure: $artifact_path"
    [[ -s "$meta_path" ]] || die "metadata sidecar missing after upload failure: $meta_path"
    [[ ! -e "$marker_path" ]] || die "upload marker was written even though upload failed"
    after_count="$(artifact_count)"
    if (( before_count > 0 )); then
        [[ "$after_count" == "$before_count" ]] || die "upload failure created duplicate local backups"
    else
        [[ "$after_count" == "1" ]] || die "expected one local backup after upload failure, found $after_count"
    fi

    if (( phase3_watch_sec > 0 )); then
        echo "Watching for duplicate local backups for ${phase3_watch_sec}s..."
        sleep "$phase3_watch_sec"
        watched_count="$(artifact_count)"
        [[ "$watched_count" == "$after_count" ]] || die "local backup count changed during upload backoff"
    fi
    stop_server

    echo "Phase 3 passed"
}

run_phase_4() {
    echo "Phase 4: recovery after upload failure"
    build_backlite
    [[ -n "$artifact_path" && -s "$artifact_path" ]] || die "phase 4 requires a local artifact from phase 3 in the same run"
    [[ ! -e "$marker_path" ]] || die "phase 4 expects the phase 3 artifact to be pending upload"
    before_count="$(artifact_count)"

    echo "Starting Backlite with backup worker enabled under recovery credentials..."
    start_server true "$recovery_aws_profile"
    echo "Waiting for /health..."
    wait_for_health
    wait_for_upload_success
    validate_uploaded_artifact true

    after_count="$(artifact_count)"
    [[ "$after_count" == "$before_count" ]] || die "upload recovery created duplicate local backups"
    stop_server

    echo "Phase 4 passed"
}

run_phase_5() {
    echo "Phase 5: restore workflow"
    build_backlite
    stop_server

    if [[ -n "$restore_artifact" ]]; then
        artifact_path="$restore_artifact"
    fi
    [[ -n "$artifact_path" && -s "$artifact_path" ]] || die "phase 5 requires a prior phase 1/4 artifact or --restore-artifact PATH"

    echo "Restoring artifact into isolated database..."
    gunzip -c "$artifact_path" >"$restore_path"
    integrity="$(sqlite3 "$restore_path" "PRAGMA integrity_check;")"
    [[ "$integrity" == "ok" ]] || die "restore integrity_check returned $integrity"
    restore_task_count="$(sqlite3 "$restore_path" "SELECT COUNT(*) FROM tasks;")"
    (( restore_task_count > 0 )) || die "restore candidate did not contain task rows"

    if [[ -f "$db_path" ]]; then
        cp "$db_path" "$db_path.before-restore"
    fi
    cp "$restore_path" "$db_path"

    echo "Starting Backlite against restored database..."
    start_server false
    echo "Waiting for /health..."
    wait_for_health
    curl -sf "$base_url/debug/stats" >"$stats_file" || die "/debug/stats failed after restore"
    api_task_count="$(curl -sf "$base_url/api/v1/tasks?limit=1" | jq '.data | length')"
    (( api_task_count > 0 )) || die "restored server did not return task rows from API"
    if [[ -n "$task_id" ]]; then
        restored_task_id="$(curl -sf "$base_url/api/v1/tasks/$task_id" | jq -r '.data.id // empty')"
        [[ "$restored_task_id" == "$task_id" ]] || die "restored server did not return seeded task $task_id"
    fi

    echo "Phase 5 passed"
}

setup_bucket_if_requested

for phase in "${phases[@]}"; do
    case "$phase" in
        1) run_phase_1 ;;
        2) run_phase_2 ;;
        3) run_phase_3 ;;
        4) run_phase_4 ;;
        5) run_phase_5 ;;
    esac
done
