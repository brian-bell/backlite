#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
tmpdir="$(mktemp -d)"
trap 'rm -rf "$tmpdir"' EXIT

fakebin="$tmpdir/bin"
mkdir -p "$fakebin"
log="$tmpdir/aws.log"

cat >"$fakebin/aws" <<'FAKEAWS'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >>"$FAKE_AWS_LOG"

case "$*" in
  *"s3api head-bucket "*)
    if [[ "${FAKE_AWS_HEAD:-missing}" == "ok" ]]; then
      exit 0
    fi
    exit 255
    ;;
  *"s3api create-bucket "*"--create-bucket-configuration "*)
    if [[ "${FAKE_AWS_CREATE_WITH_LOCATION_FAIL:-0}" == "1" ]]; then
      exit 4
    fi
    exit 0
    ;;
  *"s3api put-public-access-block "*|*"s3api put-bucket-encryption "*|*"s3api put-bucket-lifecycle-configuration "*)
    if [[ "${FAKE_AWS_OPTIONAL_FAIL:-0}" == "1" ]]; then
      exit 3
    fi
    exit 0
    ;;
  *)
    exit 0
    ;;
esac
FAKEAWS
chmod +x "$fakebin/aws"

run_setup() {
  PATH="$fakebin:$PATH" FAKE_AWS_LOG="$log" "$repo_root/scripts/setup-backup-bucket.sh" "$@"
}

assert_log_contains() {
  local expected="$1"
  if ! grep -F -- "$expected" "$log" >/dev/null; then
    echo "expected aws log to contain: $expected" >&2
    echo "--- aws log ---" >&2
    cat "$log" >&2
    exit 1
  fi
}

assert_log_not_contains() {
  local unexpected="$1"
  if grep -F -- "$unexpected" "$log" >/dev/null; then
    echo "expected aws log not to contain: $unexpected" >&2
    echo "--- aws log ---" >&2
    cat "$log" >&2
    exit 1
  fi
}

: >"$log"
FAKE_AWS_HEAD=missing run_setup \
  --bucket backlite-test \
  --region us-west-2 \
  --endpoint-url http://localhost:9000 \
  --prefix /sqlite/daily// \
  --retention-days 14 >/dev/null
assert_log_contains "--endpoint-url http://localhost:9000 --region us-west-2 s3api head-bucket --bucket backlite-test"
assert_log_contains "--endpoint-url http://localhost:9000 --region us-west-2 s3api create-bucket --bucket backlite-test --create-bucket-configuration LocationConstraint=us-west-2"
assert_log_contains "s3api put-public-access-block --bucket backlite-test"
assert_log_contains "s3api put-bucket-encryption --bucket backlite-test"
assert_log_contains "s3api put-bucket-lifecycle-configuration --bucket backlite-test"
assert_log_contains '"Prefix":"sqlite/daily/"'

: >"$log"
stderr="$tmpdir/create-retry.err"
FAKE_AWS_HEAD=missing FAKE_AWS_CREATE_WITH_LOCATION_FAIL=1 run_setup \
  --bucket compatible-create \
  --region us-west-2 \
  --endpoint-url http://localhost:9000 \
  --retention-days 0 >/dev/null 2>"$stderr"
assert_log_contains "--endpoint-url http://localhost:9000 --region us-west-2 s3api create-bucket --bucket compatible-create --create-bucket-configuration LocationConstraint=us-west-2"
assert_log_contains "--endpoint-url http://localhost:9000 --region us-west-2 s3api create-bucket --bucket compatible-create"
if ! grep -F -- "warning: provider did not accept optional bucket create location constraint" "$stderr" >/dev/null; then
  echo "expected create retry warning" >&2
  echo "--- stderr ---" >&2
  cat "$stderr" >&2
  exit 1
fi

: >"$log"
stderr="$tmpdir/existing.err"
BACKFLOW_BACKUP_S3_PREFIX=sqlite/ FAKE_AWS_HEAD=ok run_setup --bucket existing-bucket --region us-east-1 >/dev/null 2>"$stderr"
assert_log_contains "--region us-east-1 s3api head-bucket --bucket existing-bucket"
assert_log_not_contains "s3api create-bucket --bucket existing-bucket"
assert_log_not_contains "s3api put-bucket-lifecycle-configuration --bucket existing-bucket"
if ! grep -F -- "warning: existing bucket lifecycle configuration was not changed" "$stderr" >/dev/null; then
  echo "expected existing-bucket lifecycle warning" >&2
  echo "--- stderr ---" >&2
  cat "$stderr" >&2
  exit 1
fi

: >"$log"
stderr="$tmpdir/optional.err"
FAKE_AWS_HEAD=missing FAKE_AWS_OPTIONAL_FAIL=1 run_setup --bucket compatible-bucket --retention-days 7 >/dev/null 2>"$stderr"
assert_log_contains "s3api head-bucket --bucket compatible-bucket"
assert_log_contains "s3api create-bucket --bucket compatible-bucket"
if [[ "$(grep -c 'warning: provider did not accept optional' "$stderr")" -ne 3 ]]; then
  echo "expected three optional-feature warnings" >&2
  echo "--- stderr ---" >&2
  cat "$stderr" >&2
  exit 1
fi

echo "setup-backup-bucket tests passed"
