#!/usr/bin/env bash
set -uo pipefail

# Backflow AWS teardown.
#
# Removes every AWS resource created by scripts/setup-aws.sh. Resource names
# are sourced from aws-resource-names.sh so the two scripts can't drift.
#
# Defaults to dry-run. Pass --yes to actually delete. Safe to re-run if a
# previous run partially failed: every step probes before deleting and keeps
# going on errors, printing a summary at the end.

export AWS_PAGER=""

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=aws-resource-names.sh
. "$SCRIPT_DIR/aws-resource-names.sh"

DRY_RUN=1
ASSUME_YES=0
INCLUDE_FLY_USER=0

usage() {
    cat <<USAGE
Usage: $0 [--yes] [--include-fly-user] [--dry-run]

Removes AWS resources created by scripts/setup-aws.sh.

  --yes                Actually delete. Default is dry-run.
  --include-fly-user   Also delete the backflow-fly IAM user (only created when
                       BACKFLOW_PROVISION_FLY_USER=1 was set at setup time).
  --dry-run            Print actions without deleting (default).
  -h, --help           Show this help.

Scope:
  * ECS cluster ${ECS_CLUSTER} and the ${ECS_CONTAINER_NAME} / ${READER_TASK_FAMILY} task definitions
  * CloudWatch log group ${CW_LOG_GROUP}
  * EC2 launch template ${LT_NAME}, security group ${SG_NAME}
  * S3 bucket backflow-data-<account>-<region> (all versions + delete markers)
  * IAM roles ${IAM_ROLE}, ${ECS_EXECUTION_ROLE}, ${ECS_TASK_ROLE}, ${CI_ROLE}
  * IAM policies ${S3_POLICY_NAME}, ${CI_POLICY_NAME}
  * Instance profile ${IAM_ROLE}
  * GitHub OIDC provider (${OIDC_HOST}) — only if no other resources reference it
  * ECR repositories ${ECR_REPO}, ${READER_ECR_REPO}

Not touched: Supabase, Fly.io, GitHub (apart from the OIDC trust relationship
already provisioned above).
USAGE
}

while [ $# -gt 0 ]; do
    case "$1" in
        --yes|-y)
            DRY_RUN=0
            ASSUME_YES=1
            ;;
        --dry-run)
            DRY_RUN=1
            ;;
        --include-fly-user)
            INCLUDE_FLY_USER=1
            ;;
        -h|--help)
            usage
            exit 0
            ;;
        *)
            echo "unknown argument: $1" >&2
            usage
            exit 2
            ;;
    esac
    shift
done

if ! command -v aws >/dev/null 2>&1; then
    echo "ERROR: aws CLI not found on PATH" >&2
    exit 1
fi

ACCOUNT_ID=$(aws sts get-caller-identity --query Account --output text 2>/dev/null || true)
if [ -z "${ACCOUNT_ID}" ] || [ "${ACCOUNT_ID}" = "None" ]; then
    echo "ERROR: could not resolve AWS account (aws sts get-caller-identity failed)" >&2
    exit 1
fi

S3_BUCKET="$(s3_bucket_name "$ACCOUNT_ID" "$REGION")"

echo "Account: ${ACCOUNT_ID}"
echo "Region:  ${REGION}"
echo "Bucket:  ${S3_BUCKET}"
if [ "$DRY_RUN" -eq 1 ]; then
    echo "Mode:    DRY RUN (no resources will be deleted; pass --yes to actually delete)"
else
    echo "Mode:    DELETE"
fi
echo

if [ "$DRY_RUN" -eq 0 ] && [ "$ASSUME_YES" -ne 1 ]; then
    # Defense in depth; --yes already implies ASSUME_YES=1, but this guards
    # against future flag combinations that might set DRY_RUN=0 without --yes.
    read -r -p "Proceed with deletion? [type 'yes' to confirm] " reply
    if [ "$reply" != "yes" ]; then
        echo "Aborted."
        exit 0
    fi
fi

FAILURES=()

log_action() {
    if [ "$DRY_RUN" -eq 1 ]; then
        echo "    [dry-run] would: $*"
    else
        echo "    deleting: $*"
    fi
}

record_failure() {
    FAILURES+=("$1")
    echo "    WARN: $1" >&2
}

do_aws() {
    # Runs an aws CLI command unless in dry-run mode. On failure, records a
    # non-fatal failure and returns 1 so callers can decide whether to continue.
    if [ "$DRY_RUN" -eq 1 ]; then
        return 0
    fi
    if ! aws "$@" 2>/tmp/teardown-aws-err.$$; then
        local err
        err=$(cat /tmp/teardown-aws-err.$$ 2>/dev/null || true)
        rm -f /tmp/teardown-aws-err.$$
        record_failure "aws $* failed: ${err:-unknown error}"
        return 1
    fi
    rm -f /tmp/teardown-aws-err.$$
    return 0
}

# -----------------------------------------------------------------------------
# ECS: stop running tasks, deregister task definitions, delete cluster
# -----------------------------------------------------------------------------
echo "==> ECS cluster + task definitions"
if aws ecs describe-clusters --clusters "$ECS_CLUSTER" --region "$REGION" \
        --query "clusters[?status=='ACTIVE'].clusterName" --output text 2>/dev/null \
        | grep -q "$ECS_CLUSTER"; then
    # Stop any still-running tasks first so DeleteCluster doesn't fail.
    RUNNING_TASKS=$(aws ecs list-tasks --cluster "$ECS_CLUSTER" --region "$REGION" \
        --query 'taskArns' --output text 2>/dev/null || true)
    if [ -n "$RUNNING_TASKS" ] && [ "$RUNNING_TASKS" != "None" ]; then
        for arn in $RUNNING_TASKS; do
            log_action "stop ECS task $arn"
            do_aws ecs stop-task --cluster "$ECS_CLUSTER" --task "$arn" --region "$REGION" >/dev/null || true
        done
    fi

    for family in "$ECS_CONTAINER_NAME" "$READER_TASK_FAMILY"; do
        # Deregister every active revision of the family.
        ARNS=$(aws ecs list-task-definitions --family-prefix "$family" --status ACTIVE \
            --region "$REGION" --query 'taskDefinitionArns' --output text 2>/dev/null || true)
        if [ -n "$ARNS" ] && [ "$ARNS" != "None" ]; then
            for td in $ARNS; do
                log_action "deregister task definition $td"
                do_aws ecs deregister-task-definition --task-definition "$td" --region "$REGION" >/dev/null || true
            done
        fi
    done

    log_action "delete ECS cluster $ECS_CLUSTER"
    do_aws ecs delete-cluster --cluster "$ECS_CLUSTER" --region "$REGION" >/dev/null || true
else
    echo "    ECS cluster $ECS_CLUSTER not found (skipping)"
fi

# -----------------------------------------------------------------------------
# CloudWatch log group
# -----------------------------------------------------------------------------
echo "==> CloudWatch log group"
if aws logs describe-log-groups --log-group-name-prefix "$CW_LOG_GROUP" --region "$REGION" \
        --query "logGroups[?logGroupName=='${CW_LOG_GROUP}'].logGroupName" --output text 2>/dev/null \
        | grep -q "$CW_LOG_GROUP"; then
    log_action "delete log group $CW_LOG_GROUP"
    do_aws logs delete-log-group --log-group-name "$CW_LOG_GROUP" --region "$REGION" || true
else
    echo "    log group $CW_LOG_GROUP not found (skipping)"
fi

# -----------------------------------------------------------------------------
# EC2: launch template, security group, any lingering instances tagged backflow
# -----------------------------------------------------------------------------
echo "==> EC2 launch template"
if aws ec2 describe-launch-templates --launch-template-names "$LT_NAME" --region "$REGION" >/dev/null 2>&1; then
    log_action "delete launch template $LT_NAME"
    do_aws ec2 delete-launch-template --launch-template-name "$LT_NAME" --region "$REGION" >/dev/null || true
else
    echo "    launch template $LT_NAME not found (skipping)"
fi

echo "==> EC2 instances tagged backflow=true"
INSTANCE_IDS=$(aws ec2 describe-instances --region "$REGION" \
    --filters "Name=tag:backflow,Values=true" "Name=instance-state-name,Values=pending,running,stopping,stopped" \
    --query 'Reservations[].Instances[].InstanceId' --output text 2>/dev/null || true)
if [ -n "$INSTANCE_IDS" ] && [ "$INSTANCE_IDS" != "None" ]; then
    for id in $INSTANCE_IDS; do
        log_action "terminate EC2 instance $id"
        do_aws ec2 terminate-instances --instance-ids "$id" --region "$REGION" >/dev/null || true
    done
else
    echo "    no tagged EC2 instances"
fi

echo "==> EC2 security group"
SG_ID=$(aws ec2 describe-security-groups --region "$REGION" \
    --filters "Name=group-name,Values=${SG_NAME}" \
    --query 'SecurityGroups[0].GroupId' --output text 2>/dev/null || true)
if [ -n "$SG_ID" ] && [ "$SG_ID" != "None" ]; then
    log_action "delete security group $SG_ID ($SG_NAME)"
    do_aws ec2 delete-security-group --group-id "$SG_ID" --region "$REGION" || true
else
    echo "    security group $SG_NAME not found (skipping)"
fi

# -----------------------------------------------------------------------------
# S3: empty bucket (versions + delete markers), then delete
# -----------------------------------------------------------------------------
echo "==> S3 bucket $S3_BUCKET"
if aws s3api head-bucket --bucket "$S3_BUCKET" 2>/dev/null; then
    if [ "$DRY_RUN" -eq 1 ]; then
        echo "    [dry-run] would: empty bucket $S3_BUCKET (objects + versions + delete markers)"
        echo "    [dry-run] would: delete bucket $S3_BUCKET"
    else
        # Remove current objects; ignore errors on empty buckets.
        aws s3 rm "s3://${S3_BUCKET}" --recursive >/dev/null 2>&1 || true

        # Remove non-current versions + delete markers.
        VERSIONS=$(aws s3api list-object-versions --bucket "$S3_BUCKET" \
            --output json 2>/dev/null || echo '{}')
        if command -v python3 >/dev/null 2>&1; then
            DELETE_JSON=$(echo "$VERSIONS" | python3 -c '
import json, sys
data = json.load(sys.stdin) if sys.stdin.isatty() is False else {}
objects = []
for key in ("Versions", "DeleteMarkers"):
    for v in data.get(key) or []:
        objects.append({"Key": v["Key"], "VersionId": v["VersionId"]})
if objects:
    print(json.dumps({"Objects": objects, "Quiet": True}))
' 2>/dev/null || true)
            if [ -n "$DELETE_JSON" ]; then
                echo "$DELETE_JSON" | \
                    aws s3api delete-objects --bucket "$S3_BUCKET" --delete file:///dev/stdin >/dev/null 2>/tmp/teardown-aws-err.$$ || \
                    record_failure "s3api delete-objects failed: $(cat /tmp/teardown-aws-err.$$ 2>/dev/null)"
                rm -f /tmp/teardown-aws-err.$$
            fi
        fi

        log_action "delete S3 bucket $S3_BUCKET"
        do_aws s3api delete-bucket --bucket "$S3_BUCKET" --region "$REGION" || true
    fi
else
    echo "    bucket $S3_BUCKET not found (skipping)"
fi

# -----------------------------------------------------------------------------
# IAM roles, instance profile, policies
# -----------------------------------------------------------------------------
detach_role_policies() {
    local role="$1"
    local policy_arns
    policy_arns=$(aws iam list-attached-role-policies --role-name "$role" \
        --query 'AttachedPolicies[].PolicyArn' --output text 2>/dev/null || true)
    if [ -n "$policy_arns" ] && [ "$policy_arns" != "None" ]; then
        for arn in $policy_arns; do
            log_action "detach policy $arn from role $role"
            do_aws iam detach-role-policy --role-name "$role" --policy-arn "$arn" || true
        done
    fi

    local inline_names
    inline_names=$(aws iam list-role-policies --role-name "$role" \
        --query 'PolicyNames' --output text 2>/dev/null || true)
    if [ -n "$inline_names" ] && [ "$inline_names" != "None" ]; then
        for name in $inline_names; do
            log_action "delete inline policy $name from role $role"
            do_aws iam delete-role-policy --role-name "$role" --policy-name "$name" || true
        done
    fi
}

delete_role() {
    local role="$1"
    if aws iam get-role --role-name "$role" >/dev/null 2>&1; then
        detach_role_policies "$role"
        log_action "delete role $role"
        do_aws iam delete-role --role-name "$role" || true
    else
        echo "    role $role not found (skipping)"
    fi
}

delete_instance_profile() {
    local profile="$1"
    if aws iam get-instance-profile --instance-profile-name "$profile" >/dev/null 2>&1; then
        local roles
        roles=$(aws iam get-instance-profile --instance-profile-name "$profile" \
            --query 'InstanceProfile.Roles[].RoleName' --output text 2>/dev/null || true)
        for r in $roles; do
            log_action "remove role $r from instance profile $profile"
            do_aws iam remove-role-from-instance-profile --instance-profile-name "$profile" --role-name "$r" || true
        done
        log_action "delete instance profile $profile"
        do_aws iam delete-instance-profile --instance-profile-name "$profile" || true
    else
        echo "    instance profile $profile not found (skipping)"
    fi
}

delete_managed_policy() {
    local arn="$1"
    if aws iam get-policy --policy-arn "$arn" >/dev/null 2>&1; then
        # Delete all non-default versions (at most 4 extra).
        local vs
        vs=$(aws iam list-policy-versions --policy-arn "$arn" \
            --query 'Versions[?IsDefaultVersion==`false`].VersionId' --output text 2>/dev/null || true)
        for v in $vs; do
            log_action "delete policy version $v from $arn"
            do_aws iam delete-policy-version --policy-arn "$arn" --version-id "$v" || true
        done

        log_action "delete managed policy $arn"
        do_aws iam delete-policy --policy-arn "$arn" || true
    else
        echo "    policy $arn not found (skipping)"
    fi
}

echo "==> IAM instance profile ($IAM_ROLE)"
delete_instance_profile "$IAM_ROLE"

echo "==> IAM roles"
delete_role "$IAM_ROLE"
delete_role "$ECS_EXECUTION_ROLE"
delete_role "$ECS_TASK_ROLE"
delete_role "$CI_ROLE"

echo "==> IAM managed policies"
delete_managed_policy "arn:aws:iam::${ACCOUNT_ID}:policy/${S3_POLICY_NAME}"
delete_managed_policy "arn:aws:iam::${ACCOUNT_ID}:policy/${CI_POLICY_NAME}"

# -----------------------------------------------------------------------------
# IAM Fly user (only when the operator asks for it)
# -----------------------------------------------------------------------------
if [ "$INCLUDE_FLY_USER" -eq 1 ]; then
    echo "==> IAM user $FLY_USER"
    if aws iam get-user --user-name "$FLY_USER" >/dev/null 2>&1; then
        KEYS=$(aws iam list-access-keys --user-name "$FLY_USER" \
            --query 'AccessKeyMetadata[].AccessKeyId' --output text 2>/dev/null || true)
        for k in $KEYS; do
            log_action "delete access key $k for $FLY_USER"
            do_aws iam delete-access-key --user-name "$FLY_USER" --access-key-id "$k" || true
        done
        if aws iam get-user-policy --user-name "$FLY_USER" --policy-name "$FLY_USER_POLICY" >/dev/null 2>&1; then
            log_action "delete inline policy $FLY_USER_POLICY from $FLY_USER"
            do_aws iam delete-user-policy --user-name "$FLY_USER" --policy-name "$FLY_USER_POLICY" || true
        fi
        log_action "delete IAM user $FLY_USER"
        do_aws iam delete-user --user-name "$FLY_USER" || true
    else
        echo "    IAM user $FLY_USER not found (skipping)"
    fi
else
    echo "==> IAM user $FLY_USER: skipped (pass --include-fly-user to delete)"
fi

# -----------------------------------------------------------------------------
# GitHub Actions OIDC provider (only if no longer referenced)
# -----------------------------------------------------------------------------
echo "==> GitHub Actions OIDC provider"
OIDC_ARN="arn:aws:iam::${ACCOUNT_ID}:oidc-provider/${OIDC_HOST}"
if aws iam get-open-id-connect-provider --open-id-connect-provider-arn "$OIDC_ARN" >/dev/null 2>&1; then
    log_action "delete OIDC provider $OIDC_ARN"
    do_aws iam delete-open-id-connect-provider --open-id-connect-provider-arn "$OIDC_ARN" || true
else
    echo "    OIDC provider not found (skipping)"
fi

# -----------------------------------------------------------------------------
# ECR repositories (must come after ECS so running tasks aren't referencing images)
# -----------------------------------------------------------------------------
for repo in "$ECR_REPO" "$READER_ECR_REPO"; do
    echo "==> ECR repository $repo"
    if aws ecr describe-repositories --repository-names "$repo" --region "$REGION" >/dev/null 2>&1; then
        log_action "delete ECR repository $repo (including images)"
        do_aws ecr delete-repository --repository-name "$repo" --region "$REGION" --force >/dev/null || true
    else
        echo "    $repo not found (skipping)"
    fi
done

# -----------------------------------------------------------------------------
# Summary
# -----------------------------------------------------------------------------
echo
if [ "$DRY_RUN" -eq 1 ]; then
    echo "Dry run complete. Re-run with --yes to actually delete."
    exit 0
fi

if [ ${#FAILURES[@]} -eq 0 ]; then
    echo "Teardown complete. No failures."
else
    echo "Teardown finished with ${#FAILURES[@]} failure(s):"
    for f in "${FAILURES[@]}"; do
        echo "  - $f"
    done
    echo
    echo "Re-run the script to retry; each step probes for existence first so"
    echo "it is safe to invoke repeatedly."
    exit 1
fi
