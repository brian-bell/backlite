#!/usr/bin/env bash
set -euo pipefail

# Backflow AWS infrastructure setup
# Creates: ECR repo, IAM roles, security group, S3 bucket,
#          launch template (EC2 mode), and ECS cluster + task definition (Fargate mode)

REGION="${AWS_REGION:-us-east-1}"
ECR_REPO="backflow-agent"
IAM_ROLE="backflow-ec2-role"
SG_NAME="backflow-agent-sg"
LT_NAME="backflow-agent-lt"
INSTANCE_TYPE="${BACKFLOW_INSTANCE_TYPE:-m7g.xlarge}"
ECS_CLUSTER="backflow"
ECS_CONTAINER_NAME="backflow-agent"
CW_LOG_GROUP="/ecs/backflow"
ECS_EXECUTION_ROLE="backflow-ecs-execution-role"
ECS_TASK_ROLE="backflow-ecs-task-role"

ACCOUNT_ID=$(aws sts get-caller-identity --query Account --output text)

# Resolve AMI: use provided value or look up latest Amazon Linux 2023 for the instance arch
AMI_ID="${BACKFLOW_AMI:-}"
if [ -z "$AMI_ID" ]; then
    # t4g = arm64 (Graviton), t3/m5/c5 = x86_64
    if [[ "$INSTANCE_TYPE" == *g.* ]]; then
        ARCH="arm64"
    else
        ARCH="x86_64"
    fi
    echo "==> Looking up latest Amazon Linux 2023 AMI (${ARCH})..."
    AMI_ID=$(aws ec2 describe-images \
        --owners amazon \
        --filters "Name=name,Values=al2023-ami-2023.*-${ARCH}" \
                  "Name=state,Values=available" \
        --query 'sort_by(Images, &CreationDate)[-1].ImageId' \
        --output text \
        --region "$REGION")
    if [ -z "$AMI_ID" ] || [ "$AMI_ID" = "None" ]; then
        echo "ERROR: Could not find Amazon Linux 2023 AMI. Set BACKFLOW_AMI manually." >&2
        exit 1
    fi
fi
echo "    AMI: ${AMI_ID}"

echo "==> Setting up Backflow infrastructure in ${REGION}"

# --- ECR Repository ---
echo "==> Creating ECR repository..."
if aws ecr describe-repositories --repository-names "$ECR_REPO" --region "$REGION" &>/dev/null; then
    echo "    ECR repo already exists"
else
    aws ecr create-repository \
        --repository-name "$ECR_REPO" \
        --region "$REGION" \
        --image-scanning-configuration scanOnPush=true
fi

ECR_URI=$(aws ecr describe-repositories \
    --repository-names "$ECR_REPO" \
    --region "$REGION" \
    --query 'repositories[0].repositoryUri' \
    --output text)
echo "    ECR URI: ${ECR_URI}"

# --- IAM Role ---
echo "==> Creating IAM role..."
TRUST_POLICY=$(cat <<'TRUSTEOF'
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Effect": "Allow",
      "Principal": {"Service": "ec2.amazonaws.com"},
      "Action": "sts:AssumeRole"
    }
  ]
}
TRUSTEOF
)

if aws iam get-role --role-name "$IAM_ROLE" &>/dev/null; then
    echo "    IAM role already exists"
else
    aws iam create-role \
        --role-name "$IAM_ROLE" \
        --assume-role-policy-document "$TRUST_POLICY"
fi

# Attach policies for SSM and ECR
aws iam attach-role-policy \
    --role-name "$IAM_ROLE" \
    --policy-arn arn:aws:iam::aws:policy/AmazonSSMManagedInstanceCore
aws iam attach-role-policy \
    --role-name "$IAM_ROLE" \
    --policy-arn arn:aws:iam::aws:policy/AmazonEC2ContainerRegistryReadOnly

# Create instance profile
if aws iam get-instance-profile --instance-profile-name "$IAM_ROLE" &>/dev/null; then
    echo "    Instance profile already exists"
else
    aws iam create-instance-profile --instance-profile-name "$IAM_ROLE"
    aws iam add-role-to-instance-profile \
        --instance-profile-name "$IAM_ROLE" \
        --role-name "$IAM_ROLE"
fi

echo "    IAM role: ${IAM_ROLE}"

# --- Security Group ---
echo "==> Creating security group..."
VPC_ID=$(aws ec2 describe-vpcs \
    --filters "Name=isDefault,Values=true" \
    --query 'Vpcs[0].VpcId' \
    --output text \
    --region "$REGION")

SG_ID=$(aws ec2 describe-security-groups \
    --filters "Name=group-name,Values=${SG_NAME}" "Name=vpc-id,Values=${VPC_ID}" \
    --query 'SecurityGroups[0].GroupId' \
    --output text \
    --region "$REGION" 2>/dev/null) || true

if [ -z "$SG_ID" ] || [ "$SG_ID" = "None" ]; then
    SG_ID=$(aws ec2 create-security-group \
        --group-name "$SG_NAME" \
        --description "Backflow agent - outbound only" \
        --vpc-id "$VPC_ID" \
        --region "$REGION" \
        --query 'GroupId' \
        --output text)
else
    echo "    Security group already exists"
fi

# Revoke default inbound rule (no inbound needed)
aws ec2 revoke-security-group-ingress \
    --group-id "$SG_ID" \
    --protocol all \
    --source-group "$SG_ID" \
    --region "$REGION" 2>/dev/null || true

echo "    Security group: ${SG_ID}"

# --- S3 Bucket (task data: agent output, offloaded config) ---
S3_BUCKET="backflow-data-${ACCOUNT_ID}-${REGION}"
echo "==> Creating S3 bucket for task data..."
if aws s3api head-bucket --bucket "$S3_BUCKET" --region "$REGION" 2>/dev/null; then
    echo "    S3 bucket already exists"
else
    if [ "$REGION" = "us-east-1" ]; then
        aws s3api create-bucket --bucket "$S3_BUCKET" --region "$REGION"
    else
        aws s3api create-bucket --bucket "$S3_BUCKET" --region "$REGION" \
            --create-bucket-configuration LocationConstraint="$REGION"
    fi
fi

# Enable server-side encryption
aws s3api put-bucket-encryption --bucket "$S3_BUCKET" \
    --server-side-encryption-configuration '{
        "Rules": [{"ApplyServerSideEncryptionByDefault": {"SSEAlgorithm": "AES256"}}]
    }'

# Block public access
aws s3api put-public-access-block --bucket "$S3_BUCKET" \
    --public-access-block-configuration \
    'BlockPublicAcls=true,IgnorePublicAcls=true,BlockPublicPolicy=true,RestrictPublicBuckets=true'

# Lifecycle policy: expire all objects after 7 days
aws s3api put-bucket-lifecycle-configuration --bucket "$S3_BUCKET" \
    --lifecycle-configuration '{
        "Rules": [
            {
                "ID": "expire-after-7-days",
                "Status": "Enabled",
                "Filter": {},
                "Expiration": {"Days": 7}
            }
        ]
    }'

echo "    S3 bucket: ${S3_BUCKET} (lifecycle: 7-day expiration)"

# Add S3 policy to IAM role
S3_POLICY_ARN="arn:aws:iam::${ACCOUNT_ID}:policy/backflow-s3-output"
S3_POLICY=$(cat <<POLICYEOF
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Effect": "Allow",
      "Action": ["s3:PutObject", "s3:GetObject"],
      "Resource": [
        "arn:aws:s3:::${S3_BUCKET}/tasks/*",
        "arn:aws:s3:::${S3_BUCKET}/task-config/*"
      ]
    }
  ]
}
POLICYEOF
)

if aws iam get-policy --policy-arn "$S3_POLICY_ARN" 2>/dev/null; then
    echo "    S3 policy already exists, pruning old versions..."
    # IAM policies can have at most 5 versions; delete all non-default versions
    # before creating a new one.
    OLD_VERSIONS=$(aws iam list-policy-versions --policy-arn "$S3_POLICY_ARN" \
        --query 'Versions[?IsDefaultVersion==`false`].VersionId' --output text)
    for V in $OLD_VERSIONS; do
        aws iam delete-policy-version --policy-arn "$S3_POLICY_ARN" --version-id "$V"
    done
    aws iam create-policy-version \
        --policy-arn "$S3_POLICY_ARN" \
        --policy-document "$S3_POLICY" \
        --set-as-default
else
    aws iam create-policy \
        --policy-name "backflow-s3-output" \
        --policy-document "$S3_POLICY"
fi

aws iam attach-role-policy \
    --role-name "$IAM_ROLE" \
    --policy-arn "$S3_POLICY_ARN"

echo "    S3 IAM policy attached"

# --- Launch Template ---
echo "==> Creating launch template..."
USER_DATA=$(base64 < scripts/user-data.sh | tr -d '\n')

if aws ec2 describe-launch-templates \
    --launch-template-names "$LT_NAME" \
    --region "$REGION" &>/dev/null; then
    echo "    Launch template already exists, creating new version..."
    aws ec2 create-launch-template-version \
        --launch-template-name "$LT_NAME" \
        --version-description "Backflow agent updated" \
        --launch-template-data "{
            \"ImageId\": \"${AMI_ID}\",
            \"InstanceType\": \"${INSTANCE_TYPE}\",
            \"IamInstanceProfile\": {\"Name\": \"${IAM_ROLE}\"},
            \"SecurityGroupIds\": [\"${SG_ID}\"],
            \"UserData\": \"${USER_DATA}\",
            \"TagSpecifications\": [{
                \"ResourceType\": \"instance\",
                \"Tags\": [{\"Key\": \"Name\", \"Value\": \"backflow-agent\"}, {\"Key\": \"backflow\", \"Value\": \"true\"}]
            }],
            \"BlockDeviceMappings\": [{
                \"DeviceName\": \"/dev/xvda\",
                \"Ebs\": {\"VolumeSize\": 30, \"VolumeType\": \"gp3\"}
            }]
        }" \
        --region "$REGION"
else
    aws ec2 create-launch-template \
        --launch-template-name "$LT_NAME" \
        --version-description "Backflow agent v1" \
        --launch-template-data "{
            \"ImageId\": \"${AMI_ID}\",
            \"InstanceType\": \"${INSTANCE_TYPE}\",
            \"IamInstanceProfile\": {\"Name\": \"${IAM_ROLE}\"},
            \"SecurityGroupIds\": [\"${SG_ID}\"],
            \"UserData\": \"${USER_DATA}\",
            \"TagSpecifications\": [{
                \"ResourceType\": \"instance\",
                \"Tags\": [{\"Key\": \"Name\", \"Value\": \"backflow-agent\"}, {\"Key\": \"backflow\", \"Value\": \"true\"}]
            }],
            \"BlockDeviceMappings\": [{
                \"DeviceName\": \"/dev/xvda\",
                \"Ebs\": {\"VolumeSize\": 30, \"VolumeType\": \"gp3\"}
            }]
        }" \
        --region "$REGION"
fi

LT_ID=$(aws ec2 describe-launch-templates \
    --launch-template-names "$LT_NAME" \
    --query 'LaunchTemplates[0].LaunchTemplateId' \
    --output text \
    --region "$REGION")

echo "    Launch template: ${LT_ID}"

# =============================================================================
# Fargate mode infrastructure
# =============================================================================

# --- CloudWatch Log Group ---
echo "==> Creating CloudWatch log group..."
if aws logs describe-log-groups \
    --log-group-name-prefix "$CW_LOG_GROUP" \
    --region "$REGION" \
    --query "logGroups[?logGroupName=='${CW_LOG_GROUP}'].logGroupName" \
    --output text | grep -q "$CW_LOG_GROUP"; then
    echo "    Log group already exists"
else
    aws logs create-log-group \
        --log-group-name "$CW_LOG_GROUP" \
        --region "$REGION"
    aws logs put-retention-policy \
        --log-group-name "$CW_LOG_GROUP" \
        --retention-in-days 14 \
        --region "$REGION"
fi
echo "    Log group: ${CW_LOG_GROUP}"

# --- ECS Task Execution Role ---
echo "==> Creating ECS task execution role..."
ECS_TRUST_POLICY=$(cat <<'ECSEOF'
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Effect": "Allow",
      "Principal": {"Service": "ecs-tasks.amazonaws.com"},
      "Action": "sts:AssumeRole"
    }
  ]
}
ECSEOF
)

if aws iam get-role --role-name "$ECS_EXECUTION_ROLE" &>/dev/null; then
    echo "    Execution role already exists"
else
    aws iam create-role \
        --role-name "$ECS_EXECUTION_ROLE" \
        --assume-role-policy-document "$ECS_TRUST_POLICY"
fi

aws iam attach-role-policy \
    --role-name "$ECS_EXECUTION_ROLE" \
    --policy-arn arn:aws:iam::aws:policy/service-role/AmazonECSTaskExecutionRolePolicy

echo "    Execution role: ${ECS_EXECUTION_ROLE}"

# --- ECS Task Role ---
echo "==> Creating ECS task role..."
if aws iam get-role --role-name "$ECS_TASK_ROLE" &>/dev/null; then
    echo "    Task role already exists"
else
    aws iam create-role \
        --role-name "$ECS_TASK_ROLE" \
        --assume-role-policy-document "$ECS_TRUST_POLICY"
fi

# Attach S3 output policy (same bucket as EC2 mode)
aws iam attach-role-policy \
    --role-name "$ECS_TASK_ROLE" \
    --policy-arn "$S3_POLICY_ARN"

echo "    Task role: ${ECS_TASK_ROLE}"

# --- ECS Service-Linked Role ---
# Required before first ECS cluster creation in an account
echo "==> Ensuring ECS service-linked role exists..."
aws iam create-service-linked-role --aws-service-name ecs.amazonaws.com 2>/dev/null || true

# --- ECS Cluster ---
echo "==> Creating ECS cluster..."
if aws ecs describe-clusters \
    --clusters "$ECS_CLUSTER" \
    --region "$REGION" \
    --query "clusters[?status=='ACTIVE'].clusterName" \
    --output text 2>/dev/null | grep -q "$ECS_CLUSTER"; then
    echo "    ECS cluster already exists"
else
    aws ecs create-cluster \
        --cluster-name "$ECS_CLUSTER" \
        --capacity-providers FARGATE FARGATE_SPOT \
        --default-capacity-provider-strategy \
            capacityProvider=FARGATE_SPOT,weight=1 \
            capacityProvider=FARGATE,weight=0 \
        --region "$REGION"
fi
echo "    ECS cluster: ${ECS_CLUSTER}"

# --- Discover subnets ---
echo "==> Discovering subnets in default VPC..."
SUBNET_IDS=$(aws ec2 describe-subnets \
    --filters "Name=vpc-id,Values=${VPC_ID}" \
    --query 'Subnets[*].SubnetId' \
    --output text \
    --region "$REGION" | tr '\t' ',')
echo "    Subnets: ${SUBNET_IDS}"

# --- ECS Task Definition ---
echo "==> Registering ECS task definition..."
TASK_DEF=$(cat <<TDEOF
{
  "family": "${ECS_CONTAINER_NAME}",
  "networkMode": "awsvpc",
  "requiresCompatibilities": ["FARGATE"],
  "cpu": "2048",
  "memory": "8192",
  "executionRoleArn": "arn:aws:iam::${ACCOUNT_ID}:role/${ECS_EXECUTION_ROLE}",
  "taskRoleArn": "arn:aws:iam::${ACCOUNT_ID}:role/${ECS_TASK_ROLE}",
  "containerDefinitions": [
    {
      "name": "${ECS_CONTAINER_NAME}",
      "image": "${ECR_URI}:latest",
      "essential": true,
      "logConfiguration": {
        "logDriver": "awslogs",
        "options": {
          "awslogs-group": "${CW_LOG_GROUP}",
          "awslogs-region": "${REGION}",
          "awslogs-stream-prefix": "ecs"
        }
      }
    }
  ]
}
TDEOF
)

TASK_DEF_ARN=$(aws ecs register-task-definition \
    --cli-input-json "$TASK_DEF" \
    --region "$REGION" \
    --query 'taskDefinition.taskDefinitionArn' \
    --output text)
echo "    Task definition: ${TASK_DEF_ARN}"

echo ""
echo "==> Setup complete!"
echo ""
echo "For EC2 mode, add these to your .env:"
echo "  BACKFLOW_MODE=ec2"
echo "  BACKFLOW_LAUNCH_TEMPLATE_ID=${LT_ID}"
echo "  BACKFLOW_S3_BUCKET=${S3_BUCKET}"
echo "  AWS_REGION=${REGION}"
echo ""
echo "For Fargate mode, add these to your .env:"
echo "  BACKFLOW_MODE=fargate"
echo "  BACKFLOW_ECS_CLUSTER=${ECS_CLUSTER}"
echo "  BACKFLOW_ECS_TASK_DEFINITION=${ECS_CONTAINER_NAME}"
echo "  BACKFLOW_ECS_SUBNETS=${SUBNET_IDS}"
echo "  BACKFLOW_ECS_SECURITY_GROUPS=${SG_ID}"
echo "  BACKFLOW_CLOUDWATCH_LOG_GROUP=${CW_LOG_GROUP}"
echo "  BACKFLOW_S3_BUCKET=${S3_BUCKET}"
echo "  AWS_REGION=${REGION}"
echo ""
echo "Next steps:"
echo "  1. Build and push the agent image: make docker-build && make docker-push REGISTRY=${ECR_URI}"
echo "  2. Set ANTHROPIC_API_KEY and GITHUB_TOKEN in .env"
echo "  3. Run: make run"
