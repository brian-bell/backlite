#!/usr/bin/env bash
# Shared resource identifiers for setup-aws.sh / teardown-aws.sh.
# This file is sourced (not executed) so variables become part of the caller's
# environment. Keep these names in lockstep — teardown-aws.sh relies on them
# to find what setup-aws.sh created.

REGION="${AWS_REGION:-us-east-1}"

# ECR repositories
ECR_REPO="backflow-agent"
READER_ECR_REPO="backflow-reader"

# IAM
CI_ROLE="backflow-ci-deploy"
CI_POLICY_NAME="backflow-ci-ecr-push"
IAM_ROLE="backflow-ec2-role"
ECS_EXECUTION_ROLE="backflow-ecs-execution-role"
ECS_TASK_ROLE="backflow-ecs-task-role"
S3_POLICY_NAME="backflow-s3-output"
FLY_USER="backflow-fly"
FLY_USER_POLICY="${FLY_USER}-policy"
OIDC_HOST="token.actions.githubusercontent.com"

# EC2 infrastructure
SG_NAME="backflow-agent-sg"
LT_NAME="backflow-agent-lt"

# ECS / CloudWatch
ECS_CLUSTER="backflow"
ECS_CONTAINER_NAME="backflow-agent"
READER_TASK_FAMILY="backflow-reader"
CW_LOG_GROUP="/ecs/backflow"

# S3 bucket name (derived from account + region; caller must have ACCOUNT_ID).
s3_bucket_name() {
    local account="$1"
    local region="$2"
    echo "backflow-data-${account}-${region}"
}
