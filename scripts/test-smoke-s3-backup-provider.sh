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
  *"s3api head-bucket --bucket existing-bucket"*)
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
assert_contains "Phase 2" "$help_out"
assert_contains "Phase 5" "$help_out"

invalid_err="$tmpdir/invalid.err"
if scripts/smoke-s3-backup-provider.sh --bucket existing-bucket --phase 6 >/dev/null 2>"$invalid_err"; then
  echo "expected invalid phase to fail" >&2
  exit 1
fi
assert_contains "invalid phase" "$invalid_err"

phase2_out="$tmpdir/phase2.out"
phase2_err="$tmpdir/phase2.err"
AWS_LOG="$log" PATH="$fakebin:$PATH" scripts/smoke-s3-backup-provider.sh \
  --bucket existing-bucket \
  --prefix sqlite/manual-pr86/ \
  --region us-west-2 \
  --endpoint-url http://localhost:9000 \
  --phase 2 >"$phase2_out" 2>"$phase2_err"

assert_contains "Phase 2 passed" "$phase2_out"
assert_contains "existing bucket lifecycle configuration was not changed" "$phase2_err"
assert_contains "--endpoint-url http://localhost:9000 --region us-west-2 s3api head-bucket --bucket existing-bucket" "$log"
assert_contains "--endpoint-url http://localhost:9000 --region us-west-2 s3api get-bucket-lifecycle-configuration --bucket existing-bucket --output json" "$log"
assert_not_contains "put-bucket-lifecycle-configuration" "$log"

echo "smoke-s3-backup-provider tests passed"
