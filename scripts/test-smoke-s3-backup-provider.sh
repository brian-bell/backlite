#!/usr/bin/env bash
set -euo pipefail

tmpdir="$(mktemp -d)"
trap 'rm -rf "$tmpdir"' EXIT

fakebin="$tmpdir/bin"
mkdir -p "$fakebin"
log="$tmpdir/aws.log"

cat >"$fakebin/aws" <<'FAKEAWS'
#!/usr/bin/env bash
set -euo pipefail

printf '%s\n' "$*" >>"$AWS_LOG"

args="$*"
case "$args" in
  *"s3api head-bucket --bucket backlite-smoke-test"*)
    printf 'R2ENV:%s:%s\n' "${AWS_ACCESS_KEY_ID:-}" "${AWS_SECRET_ACCESS_KEY:-}" >>"$AWS_LOG"
    if [[ "${FAKE_R2_HEAD_OK:-0}" == "1" ]]; then
      exit 0
    fi
    exit 1
    ;;
  *"s3api put-public-access-block --bucket backlite-smoke-test"*|*"s3api put-bucket-encryption --bucket backlite-smoke-test"*)
    exit 0
    ;;
  *"s3api head-bucket --bucket existing-bucket"*)
    exit 0
    ;;
  *"s3api head-bucket --bucket profile-bucket"*)
    printf 'SETUP_PROFILE:%s\n' "${AWS_PROFILE:-}" >>"$AWS_LOG"
    exit 0
    ;;
  *"s3api put-public-access-block --bucket profile-bucket"*|*"s3api put-bucket-encryption --bucket profile-bucket"*)
    exit 0
    ;;
  *"s3api head-object --bucket profile-bucket --key sqlite/profile/backlite-20260626T000000Z.sqlite.gz"*)
    case "$args" in
      *"--profile prod "*|*"--profile recovery "*) ;;
      *)
        echo "head-object expected selected profile: $args" >&2
        exit 1
        ;;
    esac
    printf '{"ContentLength":123}\n'
    exit 0
    ;;
  *"s3api get-bucket-lifecycle-configuration --bucket existing-bucket"*)
    printf '{"Rules":[{"ID":"ExistingRule","Status":"Enabled","Filter":{"Prefix":"existing/"},"Expiration":{"Days":30}}]}\n'
    exit 0
    ;;
  *"s3api put-public-access-block --bucket existing-bucket"*)
    exit 0
    ;;
  *"s3api put-bucket-encryption --bucket existing-bucket"*)
    exit 0
    ;;
  *"s3api put-bucket-lifecycle-configuration --bucket existing-bucket"*)
    echo "unexpected lifecycle overwrite" >&2
    exit 1
    ;;
  *)
    echo "unexpected aws call: $args" >&2
    exit 1
    ;;
esac
FAKEAWS
chmod +x "$fakebin/aws"

cat >"$fakebin/go" <<'FAKEGO'
#!/usr/bin/env bash
set -euo pipefail

out=""
while (($# > 0)); do
  if [[ "$1" == "-o" ]]; then
    shift
    out="$1"
    break
  fi
  shift
done
[[ -n "$out" ]] || { echo "fake go expected -o" >&2; exit 1; }

cat >"$out" <<'FAKEBACKLITE'
#!/usr/bin/env bash
set -euo pipefail

printf '%s:%s\n' "$BACKFLOW_LOCAL_BACKUP_ENABLED" "${AWS_PROFILE:-}" >>"$BACKLITE_PROFILE_LOG"

if [[ "$BACKFLOW_LOCAL_BACKUP_ENABLED" == "true" ]]; then
  mkdir -p "$BACKFLOW_LOCAL_BACKUP_DIR"
  source_db="$BACKFLOW_LOCAL_BACKUP_DIR/source.sqlite"
  artifact="$BACKFLOW_LOCAL_BACKUP_DIR/backlite-20260626T000000Z.sqlite.gz"
  sqlite3 "$source_db" 'CREATE TABLE IF NOT EXISTS tasks (id TEXT PRIMARY KEY); INSERT OR IGNORE INTO tasks (id) VALUES ("bf_profile_smoke");'
  gzip -c "$source_db" >"$artifact"
  cat >"$artifact.meta.json" <<JSON
{"file_name":"backlite-20260626T000000Z.sqlite.gz","created_at":"2026-06-26T00:00:00Z","finalized_at":"2026-06-26T00:00:00Z","sha256":"fake-sha","size_bytes":123}
JSON
  if [[ "${AWS_PROFILE:-}" == "denied" ]]; then
    rm -f "$artifact.upload.json"
    printf 'failure\n' >"$BACKLITE_STATE_FILE"
  else
    cat >"$artifact.upload.json" <<JSON
{"bucket":"profile-bucket","key":"sqlite/profile/backlite-20260626T000000Z.sqlite.gz","endpoint":"","etag":"fake-etag","size_bytes":123,"sha256":"fake-sha","uploaded_at":"2026-06-26T00:00:00Z"}
JSON
    printf 'success\n' >"$BACKLITE_STATE_FILE"
  fi
else
  printf 'disabled\n' >"$BACKLITE_STATE_FILE"
fi

trap 'exit 0' TERM INT
while true; do sleep 1; done
FAKEBACKLITE
chmod +x "$out"
FAKEGO
chmod +x "$fakebin/go"

cat >"$fakebin/curl" <<'FAKECURL'
#!/usr/bin/env bash
set -euo pipefail

args="$*"
case "$args" in
  *"/health"*)
    printf '{"status":"ok"}\n'
    ;;
  *"-X POST"*"api/v1/tasks"*)
    printf '{"data":{"id":"bf_profile_smoke"}}\n'
    ;;
  *"/debug/stats"*)
    state="$(cat "${BACKLITE_STATE_FILE:-/dev/null}" 2>/dev/null || true)"
    case "$state" in
      failure)
        cat <<'JSON'
{"data":{"backup":{"upload_enabled":true,"pending_upload":true,"next_upload_attempt_at":"2026-06-26T00:01:00Z","latest_artifact":{"file_name":"backlite-20260626T000000Z.sqlite.gz"},"recent_errors":[{"phase":"upload","message":"denied"}]}}}
JSON
        ;;
      success)
        cat <<'JSON'
{"data":{"backup":{"upload_enabled":true,"pending_upload":false,"latest_artifact":{"file_name":"backlite-20260626T000000Z.sqlite.gz"},"latest_uploaded_artifact":{"bucket":"profile-bucket","key":"sqlite/profile/backlite-20260626T000000Z.sqlite.gz"},"recent_errors":[]}}}
JSON
        ;;
      *)
        cat <<'JSON'
{"data":{"backup":{"upload_enabled":true,"pending_upload":true,"recent_errors":[]}}}
JSON
        ;;
    esac
    ;;
  *)
    echo "unexpected curl call: $args" >&2
    exit 1
    ;;
esac
FAKECURL
chmod +x "$fakebin/curl"

assert_contains() {
  local needle="$1"
  local file="$2"
  if ! grep -F -- "$needle" "$file" >/dev/null; then
    echo "expected '$needle' in $file" >&2
    echo "--- $file ---" >&2
    cat "$file" >&2
    exit 1
  fi
}

assert_not_contains() {
  local needle="$1"
  local file="$2"
  if grep -F -- "$needle" "$file" >/dev/null; then
    echo "did not expect '$needle' in $file" >&2
    echo "--- $file ---" >&2
    cat "$file" >&2
    exit 1
  fi
}

help_out="$tmpdir/help.out"
scripts/smoke-s3-backup-provider.sh --help >"$help_out"
assert_contains "--phase PHASE" "$help_out"
assert_contains "--r2-bucket-url URL" "$help_out"
assert_contains "BACKFLOW_SMOKE_R2_ACCESS_KEY_ID" "$help_out"
assert_contains "Phase 2" "$help_out"
assert_contains "Phase 5" "$help_out"
assert_contains "Phase 6" "$help_out"

invalid_err="$tmpdir/invalid.err"
if scripts/smoke-s3-backup-provider.sh --bucket existing-bucket --phase 7 >/dev/null 2>"$invalid_err"; then
  echo "expected invalid phase to fail" >&2
  exit 1
fi
assert_contains "invalid phase" "$invalid_err"

r2_mismatch_err="$tmpdir/r2-mismatch.err"
if scripts/smoke-s3-backup-provider.sh \
  --bucket other-bucket \
  --r2-bucket-url https://332a424522e92bba0b6437992168064a.r2.cloudflarestorage.com/backlite-smoke-test \
  >/dev/null 2>"$r2_mismatch_err"; then
  echo "expected mismatched R2 bucket URL to fail" >&2
  exit 1
fi
assert_contains "does not match --bucket" "$r2_mismatch_err"

r2_missing_pair_err="$tmpdir/r2-missing-pair.err"
if BACKFLOW_SMOKE_R2_ACCESS_KEY_ID=r2id scripts/smoke-s3-backup-provider.sh \
  --r2-bucket-url https://332a424522e92bba0b6437992168064a.r2.cloudflarestorage.com/backlite-smoke-test \
  >/dev/null 2>"$r2_missing_pair_err"; then
  echo "expected partial R2 credentials to fail" >&2
  exit 1
fi
assert_contains "requires both BACKFLOW_SMOKE_R2_ACCESS_KEY_ID and BACKFLOW_SMOKE_R2_SECRET_ACCESS_KEY" "$r2_missing_pair_err"

r2_env_out="$tmpdir/r2-env.out"
r2_env_err="$tmpdir/r2-env.err"
r2_log="$tmpdir/r2-aws.log"
if BACKFLOW_SMOKE_R2_ACCESS_KEY_ID=r2id \
  BACKFLOW_SMOKE_R2_SECRET_ACCESS_KEY=r2secret \
  AWS_LOG="$r2_log" PATH="$fakebin:$PATH" scripts/smoke-s3-backup-provider.sh \
  --r2-bucket-url https://332a424522e92bba0b6437992168064a.r2.cloudflarestorage.com/backlite-smoke-test \
  >"$r2_env_out" 2>"$r2_env_err"; then
  echo "expected fake R2 head-bucket to fail before upload" >&2
  exit 1
fi
assert_contains "phase 6 could not access R2 bucket backlite-smoke-test" "$r2_env_err"
assert_contains "R2ENV:r2id:r2secret" "$r2_log"

profile_out="$tmpdir/profile.out"
profile_err="$tmpdir/profile.err"
profile_log="$tmpdir/profile.log"
state_file="$tmpdir/backlite-state"
if ! BACKFLOW_SMOKE_R2_ACCESS_KEY_ID=r2id \
  BACKFLOW_SMOKE_R2_SECRET_ACCESS_KEY=r2secret \
  AWS_LOG="$log" BACKLITE_PROFILE_LOG="$profile_log" BACKLITE_STATE_FILE="$state_file" PATH="$fakebin:$PATH" scripts/smoke-s3-backup-provider.sh \
  --bucket profile-bucket \
  --prefix sqlite/profile/ \
  --aws-profile prod \
  --setup-bucket \
  --phase 1 \
  --timeout 3 >"$profile_out" 2>"$profile_err"; then
  cat "$profile_out"
  cat "$profile_err" >&2
  exit 1
fi

assert_contains "Phase 1 passed" "$profile_out"
assert_contains "true:prod" "$profile_log"
assert_contains "SETUP_PROFILE:prod" "$log"

phase34_out="$tmpdir/phase34.out"
phase34_err="$tmpdir/phase34.err"
phase34_log="$tmpdir/phase34-aws.log"
phase34_profile_log="$tmpdir/phase34-profile.log"
phase34_state_file="$tmpdir/phase34-state"
if ! AWS_LOG="$phase34_log" BACKLITE_PROFILE_LOG="$phase34_profile_log" BACKLITE_STATE_FILE="$phase34_state_file" PATH="$fakebin:$PATH" scripts/smoke-s3-backup-provider.sh \
  --bucket profile-bucket \
  --prefix sqlite/profile/ \
  --failure-aws-profile denied \
  --recovery-aws-profile recovery \
  --setup-bucket \
  --phase 3,4 \
  --phase3-watch-sec 0 \
  --timeout 3 >"$phase34_out" 2>"$phase34_err"; then
  cat "$phase34_out"
  cat "$phase34_err" >&2
  exit 1
fi

assert_contains "Phase 3 passed" "$phase34_out"
assert_contains "Phase 4 passed" "$phase34_out"
assert_contains "true:denied" "$phase34_profile_log"
assert_contains "true:recovery" "$phase34_profile_log"
assert_contains "SETUP_PROFILE:recovery" "$phase34_log"
assert_contains "--profile recovery s3api head-object --bucket profile-bucket --key sqlite/profile/backlite-20260626T000000Z.sqlite.gz --output json" "$phase34_log"

recovery_phase1_out="$tmpdir/recovery-phase1.out"
recovery_phase1_err="$tmpdir/recovery-phase1.err"
recovery_phase1_log="$tmpdir/recovery-phase1-aws.log"
recovery_phase1_profile_log="$tmpdir/recovery-phase1-profile.log"
recovery_phase1_state_file="$tmpdir/recovery-phase1-state"
if AWS_LOG="$recovery_phase1_log" BACKLITE_PROFILE_LOG="$recovery_phase1_profile_log" BACKLITE_STATE_FILE="$recovery_phase1_state_file" PATH="$fakebin:$PATH" scripts/smoke-s3-backup-provider.sh \
  --bucket profile-bucket \
  --prefix sqlite/profile/ \
  --recovery-aws-profile recovery \
  --setup-bucket \
  --phase 1 \
  --timeout 3 >"$recovery_phase1_out" 2>"$recovery_phase1_err"; then
  echo "expected phase 1 without --aws-profile to fail fake head-object validation" >&2
  exit 1
fi
assert_contains "SETUP_PROFILE:" "$recovery_phase1_log"
assert_not_contains "SETUP_PROFILE:recovery" "$recovery_phase1_log"

r2_setup_out="$tmpdir/r2-setup.out"
r2_setup_err="$tmpdir/r2-setup.err"
r2_setup_log="$tmpdir/r2-setup-aws.log"
if BACKFLOW_SMOKE_R2_ACCESS_KEY_ID=r2id \
  BACKFLOW_SMOKE_R2_SECRET_ACCESS_KEY=r2secret \
  AWS_LOG="$r2_setup_log" FAKE_R2_HEAD_OK=1 PATH="$fakebin:$PATH" scripts/smoke-s3-backup-provider.sh \
  --setup-bucket \
  --r2-bucket-url https://332a424522e92bba0b6437992168064a.r2.cloudflarestorage.com/backlite-smoke-test \
  --timeout 3 >"$r2_setup_out" 2>"$r2_setup_err"; then
  echo "expected fake R2 upload path to fail after setup" >&2
  exit 1
fi
assert_contains "R2ENV:r2id:r2secret" "$r2_setup_log"
first_r2_env="$(grep -m1 '^R2ENV:' "$r2_setup_log" || true)"
if [[ "$first_r2_env" != "R2ENV:r2id:r2secret" ]]; then
  echo "expected setup helper to receive R2 credentials first, got: $first_r2_env" >&2
  echo "--- r2 setup aws log ---" >&2
  cat "$r2_setup_log" >&2
  exit 1
fi

phase2_out="$tmpdir/phase2.out"
phase2_err="$tmpdir/phase2.err"
if ! AWS_LOG="$log" PATH="$fakebin:$PATH" scripts/smoke-s3-backup-provider.sh \
  --bucket existing-bucket \
  --prefix sqlite/manual-pr86/ \
  --region us-west-2 \
  --endpoint-url http://localhost:9000 \
  --phase 2 >"$phase2_out" 2>"$phase2_err"; then
  cat "$phase2_out"
  cat "$phase2_err" >&2
  exit 1
fi

assert_contains "Phase 2 passed" "$phase2_out"
assert_contains "existing bucket lifecycle configuration was not changed" "$phase2_err"
assert_contains "--endpoint-url http://localhost:9000 --region us-west-2 s3api head-bucket --bucket existing-bucket" "$log"
assert_contains "--endpoint-url http://localhost:9000 --region us-west-2 s3api get-bucket-lifecycle-configuration --bucket existing-bucket --output json" "$log"
assert_not_contains "put-bucket-lifecycle-configuration" "$log"

echo "smoke-s3-backup-provider tests passed"
