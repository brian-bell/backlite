#!/usr/bin/env bash
set -euo pipefail

# Backflow AWS infrastructure setup
# Creates: ECR repo, IAM role, security group, launch template

REGION="${AWS_REGION:-us-east-1}"
ECR_REPO="backflow-agent"
IAM_ROLE="backflow-ec2-role"
SG_NAME="backflow-agent-sg"
LT_NAME="backflow-agent-lt"
INSTANCE_TYPE="${BACKFLOW_INSTANCE_TYPE:-m7g.xlarge}"

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

echo ""
echo "==> Setup complete!"
echo ""
echo "Add these to your environment:"
echo "  export BACKFLOW_LAUNCH_TEMPLATE_ID=${LT_ID}"
echo "  export AWS_REGION=${REGION}"
echo ""
echo "Next steps:"
echo "  1. Build and push the agent image: make docker-build && make docker-push REGISTRY=${ECR_URI}"
echo "  2. Set ANTHROPIC_API_KEY and GITHUB_TOKEN"
echo "  3. Run: make run"
